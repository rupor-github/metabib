package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"metabib/model"
)

const testDriverName = "metabib-repository-test"

var (
	testDriverMu       sync.Mutex
	testDriverHandlers = make(map[string]testQueryHandler)
)

type testQueryHandler func(query string, args []driver.NamedValue) (testRows, error)

func init() {
	sql.Register(testDriverName, testDriver{})
}

func TestInClauseAndInQuery(t *testing.T) {
	t.Parallel()

	clause, args := inClause([]int64{3, 5, 8})
	if clause != "?,?,?" {
		t.Fatalf("inClause clause = %q", clause)
	}
	if len(args) != 3 || args[0] != int64(3) || args[2] != int64(8) {
		t.Fatalf("inClause args = %#v", args)
	}

	query, args := inQuery("SELECT * FROM t WHERE id IN (", []int64{1, 2}, ") ORDER BY id")
	if query != "SELECT * FROM t WHERE id IN (?,?) ORDER BY id" {
		t.Fatalf("inQuery query = %q", query)
	}
	if len(args) != 2 {
		t.Fatalf("inQuery args = %#v", args)
	}
}

func TestFormatTime(t *testing.T) {
	t.Parallel()

	if got := formatTime(sql.NullTime{}); got != "" {
		t.Fatalf("formatTime(invalid) = %q", got)
	}
	tm := time.Date(2026, 6, 20, 12, 30, 45, 0, time.UTC)
	if got := formatTime(sql.NullTime{Time: tm, Valid: true}); got != "2026-06-20T12:30:45Z" {
		t.Fatalf("formatTime(valid) = %q", got)
	}
}

func TestFileIdentityFallback(t *testing.T) {
	t.Parallel()

	name := "123.fb2"
	stem := strings.TrimSuffix(name, ".fb2")
	if stem != "123" {
		t.Fatalf("stem = %q", stem)
	}
}

func TestImportProvenanceRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := "provenance-round-trip"
	var payload string
	testDriverMu.Lock()
	testDriverHandlers[dsn] = func(query string, args []driver.NamedValue) (testRows, error) {
		query = strings.Join(strings.Fields(query), " ")
		switch {
		case strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS"):
			return testRows{}, nil
		case strings.HasPrefix(query, "REPLACE INTO"):
			payload, _ = args[0].Value.(string)
			return testRows{}, nil
		case strings.Contains(query, "SELECT payload FROM"):
			return rows([]string{"payload"}, []driver.Value{payload}), nil
		default:
			return testRows{}, fmt.Errorf("unexpected query: %s", query)
		}
	}
	testDriverMu.Unlock()
	t.Cleanup(func() {
		testDriverMu.Lock()
		delete(testDriverHandlers, dsn)
		testDriverMu.Unlock()
	})
	sqldb, err := sql.Open(testDriverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	repo := &Repository{db: sqldb, tables: make(map[string]bool), cols: make(map[string]map[string]bool)}
	want := ImportProvenance{
		DumpDir:  "/dumps",
		DumpDate: "2026-06-20",
		Dumps: []ImportDumpProvenance{{
			Path:          "/dumps/libbook.sql",
			Name:          "libbook.sql",
			DumpDate:      "2026-06-20",
			DumpCompleted: "2026-06-20T02:19:33",
			Modified:      "2026-06-20T02:19:34Z",
			MD5:           "abc123",
		}},
	}

	if err := repo.WriteImportProvenance(ctx, want); err != nil {
		t.Fatalf("WriteImportProvenance() error = %v", err)
	}
	got, err := repo.ImportProvenance(ctx)
	if err != nil {
		t.Fatalf("ImportProvenance() error = %v", err)
	}
	if got.Schema != importProvenanceSchema || got.DumpDir != want.DumpDir || got.Dumps[0].MD5 != want.Dumps[0].MD5 {
		t.Fatalf("ImportProvenance() = %#v, want %#v", got, want)
	}
}

func TestImportProvenanceMissing(t *testing.T) {
	t.Parallel()

	repo := newTestRepository(t)
	repo.tables[importProvenanceTable] = false
	_, err := repo.ImportProvenance(context.Background())
	if !errors.Is(err, ErrImportProvenanceNotFound) {
		t.Fatalf("ImportProvenance() error = %v, want ErrImportProvenanceNotFound", err)
	}
}

func TestContributorFromClause(t *testing.T) {
	t.Parallel()

	direct := contributorFromClause("libavtor", "AvtorId", false)
	if !strings.Contains(direct, "JOIN libavtorname n ON n.AvtorId = a.AvtorId") || strings.Contains(direct, "libavtoraliase") {
		t.Fatalf("direct clause = %q", direct)
	}
	aliased := contributorFromClause("libavtor", "AvtorId", true)
	if !strings.Contains(aliased, "LEFT JOIN libavtoraliase aa ON aa.BadId = a.AvtorId") {
		t.Fatalf("alias clause missing alias join: %q", aliased)
	}
	if !strings.Contains(aliased, "n.AvtorId = COALESCE(aa.GoodId, a.AvtorId)") {
		t.Fatalf("alias clause missing canonical join: %q", aliased)
	}
}

func TestRepositoryOrderClauses(t *testing.T) {
	t.Parallel()

	query, _ := inQuery(`SELECT BookId, FileName FROM libfilename WHERE BookId IN (`, []int64{1, 2}, `) ORDER BY BookId, FileName`)
	if !strings.Contains(query, "ORDER BY BookId, FileName") {
		t.Fatalf("file identity query = %q", query)
	}
	batchQuery, _ := inQuery(`
SELECT a.BookId, n.AvtorId, n.FirstName, n.MiddleName, n.LastName, n.NickName,
       n.uid, n.Email, n.Homepage, n.Gender, n.MasterId, a.Pos
	  FROM `+contributorFromClause("libavtor", "AvtorId", false)+`
	 WHERE a.BookId IN (`, []int64{1, 2}, `) ORDER BY a.BookId, a.Pos, n.AvtorId`)
	if !strings.Contains(batchQuery, "ORDER BY a.BookId, a.Pos, n.AvtorId") {
		t.Fatalf("batch contributor query = %q", batchQuery)
	}
	singleQuery := `
SELECT n.AvtorId, n.FirstName, n.MiddleName, n.LastName, n.NickName, n.uid,
       n.Email, n.Homepage, n.Gender, n.MasterId, a.Pos
	  FROM ` + contributorFromClause("libavtor", "AvtorId", false) + `
	 WHERE a.BookId = ?
  ORDER BY a.Pos, n.AvtorId`
	if !strings.Contains(singleQuery, "ORDER BY a.Pos, n.AvtorId") {
		t.Fatalf("single contributor query = %q", singleQuery)
	}
}

func TestBookSourcesByIDsPopulatesAllDatabaseFields(t *testing.T) {
	repo := newTestRepository(t)

	sources, err := repo.BookSourcesByIDs(context.Background(), []int64{1})
	if err != nil {
		t.Fatalf("BookSourcesByIDs() error = %v", err)
	}
	assertFullDatabaseSource(t, sources[1])
}

func TestBookByIDPopulatesAllDatabaseFields(t *testing.T) {
	repo := newTestRepository(t)

	source, err := repo.BookByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("BookByID() error = %v", err)
	}
	assertFullDatabaseSource(t, source)
}

func TestLibrusecCurrentRepositoryMapping(t *testing.T) {
	repo := newLibrusecTestRepository(t)

	ids, err := repo.BookIDs(context.Background())
	if err != nil {
		t.Fatalf("BookIDs() error = %v", err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("BookIDs() = %v, want [1]", ids)
	}
	sources, err := repo.BookSourcesByIDs(context.Background(), []int64{1})
	if err != nil {
		t.Fatalf("BookSourcesByIDs() error = %v", err)
	}
	source := sources[1]
	if !source.Present || source.Book == nil || source.Book.BookID != 1 || source.Book.ReplacedBy != 2 {
		t.Fatalf("book source = %#v", source)
	}
	if len(source.Authors) != 1 || source.Authors[0].ID != 100 || source.Authors[0].LastName != "Author" {
		t.Fatalf("authors = %#v", source.Authors)
	}
	if len(source.Translators) != 1 || source.Translators[0].ID != 200 || source.Translators[0].LastName != "Translator" {
		t.Fatalf("translators = %#v", source.Translators)
	}
	if len(source.Illustrators) != 1 || source.Illustrators[0].ID != 300 || source.Illustrators[0].LastName != "Illustrator" {
		t.Fatalf("illustrators = %#v", source.Illustrators)
	}
	if len(source.Genres) != 1 || source.Genres[0].ID != 30 || source.Genres[0].Code != "sf" || source.Genres[0].Description != "Science fiction" {
		t.Fatalf("genres = %#v", source.Genres)
	}
	if len(source.Sequences) != 1 || source.Sequences[0].ID != 40 || source.Sequences[0].Name != "Cycle" || source.Sequences[0].Number != 7 || source.Sequences[0].Type != 0 {
		t.Fatalf("sequences = %#v", source.Sequences)
	}
	if source.Rating == nil || source.Rating.Average != 4 || source.Rating.Count != 5 {
		t.Fatalf("rating = %#v", source.Rating)
	}
}

func assertFullDatabaseSource(t *testing.T, source model.DatabaseSource) {
	t.Helper()
	if !source.Present || source.Book == nil {
		t.Fatalf("source missing book: %#v", source)
	}
	book := source.Book
	if book.BookID != 1 || book.FileSize != 123 || book.Time != "2026-06-22T01:02:03Z" || book.Title != "DB title" || book.Lang != "ru" || book.SrcLang != "en" || book.FileType != "fb2" || book.Year != 1972 || book.Deleted != "0" || book.FileAuthor != "file author" || book.Keywords != "keywords" || book.MD5 != "abc" || book.Modified != "2026-06-23T04:05:06Z" || book.ReplacedBy != 2 {
		t.Fatalf("book = %#v", book)
	}
	if len(source.Authors) != 1 || source.Authors[0].ID != 10 || source.Authors[0].FirstName != "First" || source.Authors[0].MiddleName != "Middle" || source.Authors[0].LastName != "Last" || source.Authors[0].NickName != "Nick" || source.Authors[0].UID != 11 || source.Authors[0].Email != "a@example.org" || source.Authors[0].Homepage != "https://example.org" || source.Authors[0].Gender != "m" || source.Authors[0].MasterID != 12 || source.Authors[0].Position != 1 {
		t.Fatalf("authors = %#v", source.Authors)
	}
	if len(source.Translators) != 1 || source.Translators[0].ID != 20 || source.Translators[0].FirstName != "Tr" || source.Translators[0].MiddleName != "TM" || source.Translators[0].LastName != "Person" || source.Translators[0].NickName != "TNick" || source.Translators[0].UID != 21 || source.Translators[0].Email != "t@example.org" || source.Translators[0].Homepage != "https://example.net" || source.Translators[0].Gender != "f" || source.Translators[0].MasterID != 22 || source.Translators[0].Position != 2 {
		t.Fatalf("translators = %#v", source.Translators)
	}
	if len(source.Genres) != 1 || source.Genres[0].ID != 30 || source.Genres[0].Code != "sf" || source.Genres[0].TranslatedCode != "sci-fi" || source.Genres[0].Description != "Science fiction" || source.Genres[0].Meta != "meta" {
		t.Fatalf("genres = %#v", source.Genres)
	}
	if len(source.Sequences) != 1 || source.Sequences[0].ID != 40 || source.Sequences[0].Name != "Cycle" || source.Sequences[0].Number != 1 || source.Sequences[0].Level != 2 || source.Sequences[0].Type != 3 {
		t.Fatalf("sequences = %#v", source.Sequences)
	}
	if source.Rating == nil || source.Rating.Average != 4 || source.Rating.Count != 5 || source.Rating.Min != 1 || source.Rating.Max != 5 {
		t.Fatalf("rating = %#v", source.Rating)
	}
	if len(source.Filenames) != 1 || source.Filenames[0] != "1.fb2" {
		t.Fatalf("filenames = %#v", source.Filenames)
	}
	if len(source.JoinedBooks) != 1 || source.JoinedBooks[0].ID != 50 || source.JoinedBooks[0].Time != "2026-06-24T07:08:09Z" || source.JoinedBooks[0].BadID != 1 || source.JoinedBooks[0].GoodID != 2 || source.JoinedBooks[0].RealID != 3 {
		t.Fatalf("joined books = %#v", source.JoinedBooks)
	}
}

func newTestRepository(t *testing.T) *Repository {
	t.Helper()
	dsn := t.Name()
	testDriverMu.Lock()
	testDriverHandlers[dsn] = repositoryTestRows
	testDriverMu.Unlock()
	t.Cleanup(func() {
		testDriverMu.Lock()
		delete(testDriverHandlers, dsn)
		testDriverMu.Unlock()
	})
	db, err := sql.Open(testDriverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Repository{
		db: db,
		tables: map[string]bool{
			"libbook":           true,
			"libavtor":          true,
			"libtranslator":     true,
			"libavtorname":      true,
			"libavtoraliase":    false,
			"libgenre":          true,
			"libgenrelist":      true,
			"libgenretranslate": true,
			"libseq":            true,
			"libseqname":        true,
			"librate":           true,
			"libfilename":       true,
			"libjoinedbooks":    true,
		},
		cols: map[string]map[string]bool{"libbook": {"ReplacedBy": true}},
	}
}

func newLibrusecTestRepository(t *testing.T) *Repository {
	t.Helper()
	dsn := t.Name()
	testDriverMu.Lock()
	testDriverHandlers[dsn] = librusecRepositoryTestRows
	testDriverMu.Unlock()
	t.Cleanup(func() {
		testDriverMu.Lock()
		delete(testDriverHandlers, dsn)
		testDriverMu.Unlock()
	})
	db, err := sql.Open(testDriverName, dsn)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Repository{
		db:     db,
		format: FormatLibrusecCurrent,
		tables: map[string]bool{
			"libbook":        true,
			"libavtor":       true,
			"libavtors":      true,
			"libgenre":       true,
			"libgenres":      true,
			"libseq":         true,
			"libseqs":        true,
			"librate":        true,
			"libfilename":    false,
			"libjoinedbooks": false,
		},
		cols: map[string]map[string]bool{"libbook": {"ReplacedBy": true}},
	}
}

func librusecRepositoryTestRows(query string, args []driver.NamedValue) (testRows, error) {
	query = strings.Join(strings.Fields(query), " ")
	bookTime := time.Date(2026, 7, 12, 1, 2, 3, 0, time.UTC)
	modifiedTime := time.Date(2026, 7, 13, 4, 5, 6, 0, time.UTC)
	switch {
	case strings.Contains(query, "SELECT bid FROM libbook"):
		return rows([]string{"bid"}, []driver.Value{int64(1)}), nil
	case strings.Contains(query, "FROM libbook WHERE bid IN"):
		return rows(
			[]string{"bid", "FileSize", "Time", "Title", "Lang", "SrcLang", "FileType", "Year", "Deleted", "FileAuthor", "keywords", "md5", "Modified", "ReplacedBy"},
			[]driver.Value{int64(1), int64(123), bookTime, "DB title", "ru", "en", "fb2", int64(1972), "0", "file author", "keywords", "abc", modifiedTime, int64(2)},
		), nil
	case strings.Contains(query, "FROM libavtor a"):
		if !strings.Contains(query, "COALESCE(NULLIF(n.main, 0), n.aid)") {
			return testRows{}, fmt.Errorf("missing main alias join: %s", query)
		}
		role, _ := args[len(args)-1].Value.(string)
		switch role {
		case "a":
			return rows(librusecContributorBatchColumns(), append([]driver.Value{int64(1)}, librusecContributorValues(100, "Author")...)), nil
		case "t":
			return rows(librusecContributorBatchColumns(), append([]driver.Value{int64(1)}, librusecContributorValues(200, "Translator")...)), nil
		case "i":
			return rows(librusecContributorBatchColumns(), append([]driver.Value{int64(1)}, librusecContributorValues(300, "Illustrator")...)), nil
		}
		return testRows{}, fmt.Errorf("unexpected Librusec role %q", role)
	case strings.Contains(query, "FROM libgenre g JOIN libgenres"):
		return rows([]string{"bid", "gid", "code", "translated", "gdesc", "meta"}, []driver.Value{int64(1), int64(30), "sf", "", "Science fiction", ""}), nil
	case strings.Contains(query, "FROM libseq s JOIN libseqs"):
		return rows([]string{"bid", "sid", "seqname", "sn", "level", "type"}, []driver.Value{int64(1), int64(40), "Cycle", "7.90", int64(0), int64(0)}), nil
	case strings.Contains(query, "FROM librate WHERE bid IN"):
		return rows([]string{"bid", "Average", "Count", "Min", "Max"}, []driver.Value{int64(1), float64(4), int64(5), int64(1), int64(5)}), nil
	default:
		return testRows{}, fmt.Errorf("unexpected query: %s", query)
	}
}

func librusecContributorBatchColumns() []string {
	return append([]string{"bid"}, contributorColumns()...)
}

func librusecContributorValues(id int64, lastName string) []driver.Value {
	return []driver.Value{id, "First", "Middle", lastName, "Nick", int64(11), "a@example.org", "https://example.org", "m", int64(0), int64(0)}
}

func repositoryTestRows(query string, _ []driver.NamedValue) (testRows, error) {
	query = strings.Join(strings.Fields(query), " ")
	bookTime := time.Date(2026, 6, 22, 1, 2, 3, 0, time.UTC)
	modifiedTime := time.Date(2026, 6, 23, 4, 5, 6, 0, time.UTC)
	joinedTime := time.Date(2026, 6, 24, 7, 8, 9, 0, time.UTC)
	switch {
	case strings.Contains(query, "FROM libbook WHERE BookId IN"):
		return rows(
			[]string{"BookId", "FileSize", "Time", "Title", "Lang", "SrcLang", "FileType", "Year", "Deleted", "FileAuthor", "keywords", "md5", "Modified", "ReplacedBy"},
			[]driver.Value{int64(1), int64(123), bookTime, "DB title", "ru", "en", "fb2", int64(1972), "0", "file author", "keywords", "abc\x00\x00", modifiedTime, int64(2)},
		), nil
	case strings.Contains(query, "FROM libbook WHERE BookId ="):
		return rows(
			[]string{"BookId", "FileSize", "Time", "Title", "Lang", "SrcLang", "FileType", "Year", "Deleted", "FileAuthor", "keywords", "md5", "Modified", "ReplacedBy"},
			[]driver.Value{int64(1), int64(123), bookTime, "DB title", "ru", "en", "fb2", int64(1972), "0", "file author", "keywords", "abc\x00\x00", modifiedTime, int64(2)},
		), nil
	case strings.Contains(query, "FROM libavtor a"):
		if strings.Contains(query, "a.BookId IN") {
			return rows(contributorBatchColumns(), append([]driver.Value{int64(1)}, authorValues()...)), nil
		}
		return rows(contributorColumns(), authorValues()), nil
	case strings.Contains(query, "FROM libtranslator a"):
		if strings.Contains(query, "a.BookId IN") {
			return rows(contributorBatchColumns(), append([]driver.Value{int64(1)}, translatorValues()...)), nil
		}
		return rows(contributorColumns(), translatorValues()), nil
	case strings.Contains(query, "FROM libgenre g"):
		if strings.Contains(query, "g.BookId IN") {
			return rows([]string{"BookId", "GenreId", "GenreCode", "trgGenreCode", "GenreDesc", "GenreMeta"}, []driver.Value{int64(1), int64(30), "sf", "sci-fi", "Science fiction", "meta"}), nil
		}
		return rows([]string{"GenreId", "GenreCode", "trgGenreCode", "GenreDesc", "GenreMeta"}, []driver.Value{int64(30), "sf", "sci-fi", "Science fiction", "meta"}), nil
	case strings.Contains(query, "FROM libseq s"):
		if strings.Contains(query, "s.BookId IN") {
			return rows([]string{"BookId", "SeqId", "SeqName", "SeqNumb", "Level", "Type"}, []driver.Value{int64(1), int64(40), "Cycle", int64(1), int64(2), int64(3)}), nil
		}
		return rows([]string{"SeqId", "SeqName", "SeqNumb", "Level", "Type"}, []driver.Value{int64(40), "Cycle", int64(1), int64(2), int64(3)}), nil
	case strings.Contains(query, "FROM librate WHERE BookId IN"):
		return rows([]string{"BookId", "Average", "Count", "Min", "Max"}, []driver.Value{int64(1), float64(4), int64(5), int64(1), int64(5)}), nil
	case strings.Contains(query, "FROM librate WHERE BookId ="):
		return rows([]string{"Average", "Count", "Min", "Max"}, []driver.Value{float64(4), int64(5), int64(1), int64(5)}), nil
	case strings.Contains(query, "FROM libfilename WHERE BookId IN"):
		return rows([]string{"BookId", "FileName"}, []driver.Value{int64(1), "1.fb2"}), nil
	case strings.Contains(query, "FROM libfilename WHERE BookId ="):
		return rows([]string{"FileName"}, []driver.Value{"1.fb2"}), nil
	case strings.Contains(query, "FROM libjoinedbooks WHERE BadId IN"):
		return rows([]string{"Id", "Time", "BadId", "GoodId", "realId"}, []driver.Value{int64(50), joinedTime, int64(1), int64(2), int64(3)}), nil
	case strings.Contains(query, "FROM libjoinedbooks WHERE BadId ="):
		return rows([]string{"Id", "Time", "BadId", "GoodId", "realId"}, []driver.Value{int64(50), joinedTime, int64(1), int64(2), int64(3)}), nil
	default:
		return testRows{}, fmt.Errorf("unexpected query: %s", query)
	}
}

func contributorBatchColumns() []string {
	return append([]string{"BookId"}, contributorColumns()...)
}

func contributorColumns() []string {
	return []string{"AvtorId", "FirstName", "MiddleName", "LastName", "NickName", "uid", "Email", "Homepage", "Gender", "MasterId", "Pos"}
}

func authorValues() []driver.Value {
	return []driver.Value{int64(10), "First", "Middle", "Last", "Nick", int64(11), "a@example.org", "https://example.org", "m", int64(12), int64(1)}
}

func translatorValues() []driver.Value {
	return []driver.Value{int64(20), "Tr", "TM", "Person", "TNick", int64(21), "t@example.org", "https://example.net", "f", int64(22), int64(2)}
}

func rows(columns []string, row []driver.Value) testRows {
	return testRows{columns: columns, values: [][]driver.Value{row}}
}

type testDriver struct{}

func (testDriver) Open(name string) (driver.Conn, error) {
	testDriverMu.Lock()
	handler := testDriverHandlers[name]
	testDriverMu.Unlock()
	if handler == nil {
		return nil, fmt.Errorf("missing test SQL handler for %q", name)
	}
	return testConn{handler: handler}, nil
}

type testConn struct {
	handler testQueryHandler
}

func (c testConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare is not implemented")
}

func (c testConn) Close() error { return nil }

func (c testConn) Begin() (driver.Tx, error) { return nil, errors.New("Begin is not implemented") }

func (c testConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.handler(query, args)
	if err != nil {
		return nil, err
	}
	return &rows, nil
}

func (c testConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := c.handler(query, args); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

type testRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *testRows) Columns() []string { return r.columns }

func (r *testRows) Close() error { return nil }

func (r *testRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
