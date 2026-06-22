package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"metabib/config"
	"metabib/model"
)

type Repository struct {
	db     *sql.DB
	tables map[string]bool
	cols   map[string]map[string]bool
	tmu    sync.RWMutex
}

type FileIdentity struct {
	FileName  string
	Extension string
}

func Open(ctx context.Context, cfg config.DatabaseConfig) (*Repository, error) {
	dsn, err := DSN(cfg, true)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open MariaDB connection: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping MariaDB connection: %w", err)
	}
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)
	}
	return &Repository{db: db, tables: make(map[string]bool), cols: make(map[string]map[string]bool)}, nil
}

func (r *Repository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

func (r *Repository) BookIDs(ctx context.Context) ([]int64, error) {
	query := "SELECT BookId FROM libbook WHERE FileType = 'fb2' ORDER BY BookId"
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query book ids: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan book id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) BookIDByFilename(ctx context.Context, filename string) (int64, error) {
	if ok, err := r.tableExists(ctx, "libfilename"); err != nil || !ok {
		return 0, err
	}
	var id int64
	if err := r.db.QueryRowContext(ctx, "SELECT BookId FROM libfilename WHERE FileName = ?", filename).Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("query book id for filename %q: %w", filename, err)
	}
	return id, nil
}

func (r *Repository) FileIdentitiesByIDs(ctx context.Context, ids []int64) (map[int64]FileIdentity, error) {
	out := make(map[int64]FileIdentity, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	books, err := r.books(ctx, ids)
	if err != nil {
		return nil, err
	}
	for id, book := range books {
		if strings.EqualFold(book.FileType, "fb2") {
			out[id] = FileIdentity{FileName: strconv.FormatInt(id, 10), Extension: book.FileType}
		} else {
			out[id] = FileIdentity{FileName: strconv.FormatInt(id, 10), Extension: book.FileType}
		}
	}
	if ok, err := r.tableExists(ctx, "libfilename"); err != nil || !ok {
		return out, err
	}
	query, args := inQuery(`SELECT BookId, FileName FROM libfilename WHERE BookId IN (`, ids, `) ORDER BY BookId`)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query file identities: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan file identity: %w", err)
		}
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		ext := strings.TrimPrefix(filepath.Ext(name), ".")
		out[id] = FileIdentity{FileName: stem, Extension: ext}
	}
	return out, rows.Err()
}

func (r *Repository) BookByID(ctx context.Context, id int64) (model.DatabaseSource, error) {
	src := model.DatabaseSource{}
	book, present, err := r.book(ctx, id)
	if err != nil || !present {
		return src, err
	}
	src.Present = true
	src.Book = book
	if src.Authors, err = r.contributors(ctx, id, "libavtor", "AvtorId"); err != nil {
		return src, err
	}
	if src.Translators, err = r.contributors(ctx, id, "libtranslator", "TranslatorId"); err != nil {
		return src, err
	}
	if src.Genres, err = r.genres(ctx, id); err != nil {
		return src, err
	}
	if src.Sequences, err = r.sequences(ctx, id); err != nil {
		return src, err
	}
	if src.Rating, err = r.rating(ctx, id); err != nil {
		return src, err
	}
	if src.Filenames, err = r.filenames(ctx, id); err != nil {
		return src, err
	}
	if src.JoinedBooks, err = r.joinedBooks(ctx, id); err != nil {
		return src, err
	}
	return src, nil
}

func (r *Repository) BookSourcesByIDs(ctx context.Context, ids []int64) (map[int64]model.DatabaseSource, error) {
	out := make(map[int64]model.DatabaseSource, len(ids))
	for _, id := range ids {
		out[id] = model.DatabaseSource{}
	}
	if len(ids) == 0 {
		return out, nil
	}
	books, err := r.books(ctx, ids)
	if err != nil {
		return nil, err
	}
	for id, book := range books {
		src := out[id]
		src.Present = true
		src.Book = book
		out[id] = src
	}
	if err := r.attachContributors(ctx, ids, out, "libavtor", "AvtorId", func(src *model.DatabaseSource, v []model.Contributor) { src.Authors = v }); err != nil {
		return nil, err
	}
	if err := r.attachContributors(ctx, ids, out, "libtranslator", "TranslatorId", func(src *model.DatabaseSource, v []model.Contributor) { src.Translators = v }); err != nil {
		return nil, err
	}
	if err := r.attachGenres(ctx, ids, out); err != nil {
		return nil, err
	}
	if err := r.attachSequences(ctx, ids, out); err != nil {
		return nil, err
	}
	if err := r.attachRatings(ctx, ids, out); err != nil {
		return nil, err
	}
	if err := r.attachFilenames(ctx, ids, out); err != nil {
		return nil, err
	}
	if err := r.attachJoinedBooks(ctx, ids, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repository) books(ctx context.Context, ids []int64) (map[int64]*model.DBBook, error) {
	if ok, err := r.tableExists(ctx, "libbook"); err != nil || !ok {
		return nil, err
	}
	replacedBy, err := r.columnExists(ctx, "libbook", "ReplacedBy")
	if err != nil {
		return nil, err
	}
	extra := ""
	if replacedBy {
		extra = ", ReplacedBy"
	}
	query, args := inQuery(`
SELECT BookId, FileSize, Time, Title, Lang, SrcLang,
       FileType, Year, Deleted, FileAuthor, keywords, CAST(md5 AS CHAR),
       Modified`+extra+`
  FROM libbook WHERE BookId IN (`, ids, `)`)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query book batch: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]*model.DBBook, len(ids))
	for rows.Next() {
		var b model.DBBook
		var tm, modified sql.NullTime
		dest := []any{&b.BookID, &b.FileSize, &tm, &b.Title, &b.Lang,
			&b.SrcLang, &b.FileType, &b.Year, &b.Deleted, &b.FileAuthor,
			&b.Keywords, &b.MD5, &modified}
		if replacedBy {
			dest = append(dest, &b.ReplacedBy)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scan book batch: %w", err)
		}
		b.Time = formatTime(tm)
		b.Modified = formatTime(modified)
		b.MD5 = strings.TrimRight(b.MD5, "\x00")
		out[b.BookID] = &b
	}
	return out, rows.Err()
}

func (r *Repository) attachContributors(ctx context.Context, ids []int64, out map[int64]model.DatabaseSource, table string, idColumn string, set func(*model.DatabaseSource, []model.Contributor)) error {
	if ok, err := r.tableExists(ctx, table); err != nil || !ok {
		return err
	}
	if ok, err := r.tableExists(ctx, "libavtorname"); err != nil || !ok {
		return err
	}
	useAliases, err := r.useAuthorAliases(ctx, table)
	if err != nil {
		return err
	}
	query, args := inQuery(fmt.Sprintf(`
SELECT a.BookId, n.AvtorId, n.FirstName, n.MiddleName, n.LastName, n.NickName,
       n.uid, n.Email, n.Homepage, n.Gender, n.MasterId, a.Pos
	  FROM %s
	 WHERE a.BookId IN (`, contributorFromClause(table, idColumn, useAliases)), ids, `) ORDER BY a.BookId`)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query contributors batch from %s: %w", table, err)
	}
	defer rows.Close()
	grouped := make(map[int64][]model.Contributor)
	for rows.Next() {
		var bookID int64
		var c model.Contributor
		if err := rows.Scan(&bookID, &c.ID, &c.FirstName, &c.MiddleName, &c.LastName, &c.NickName, &c.UID, &c.Email, &c.Homepage, &c.Gender, &c.MasterID, &c.Position); err != nil {
			return fmt.Errorf("scan contributor batch: %w", err)
		}
		grouped[bookID] = append(grouped[bookID], c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for id, values := range grouped {
		src := out[id]
		set(&src, values)
		out[id] = src
	}
	return nil
}

func (r *Repository) attachGenres(ctx context.Context, ids []int64, out map[int64]model.DatabaseSource) error {
	if ok, err := r.tableExists(ctx, "libgenre"); err != nil || !ok {
		return err
	}
	if ok, err := r.tableExists(ctx, "libgenrelist"); err != nil || !ok {
		return err
	}
	prefix := `
SELECT g.BookId, gl.GenreId, gl.GenreCode, '', gl.GenreDesc, gl.GenreMeta
  FROM libgenre g JOIN libgenrelist gl ON gl.GenreId = g.GenreId
 WHERE g.BookId IN (`
	suffix := `) ORDER BY g.BookId`
	if ok, err := r.tableExists(ctx, "libgenretranslate"); err != nil {
		return err
	} else if ok {
		prefix = `
SELECT g.BookId, gl.GenreId, gl.GenreCode, COALESCE(gt.trgGenreCode, ''), gl.GenreDesc, gl.GenreMeta
  FROM libgenre g JOIN libgenrelist gl ON gl.GenreId = g.GenreId
  LEFT JOIN libgenretranslate gt ON gt.srcGenreCode = gl.GenreCode
 WHERE g.BookId IN (`
	}
	query, args := inQuery(prefix, ids, suffix)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query genres batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bookID int64
		var g model.DBGenre
		if err := rows.Scan(&bookID, &g.ID, &g.Code, &g.TranslatedCode, &g.Description, &g.Meta); err != nil {
			return fmt.Errorf("scan genre batch: %w", err)
		}
		src := out[bookID]
		src.Genres = append(src.Genres, g)
		out[bookID] = src
	}
	return rows.Err()
}

func (r *Repository) attachSequences(ctx context.Context, ids []int64, out map[int64]model.DatabaseSource) error {
	if ok, err := r.tableExists(ctx, "libseq"); err != nil || !ok {
		return err
	}
	if ok, err := r.tableExists(ctx, "libseqname"); err != nil || !ok {
		return err
	}
	query, args := inQuery(`
SELECT s.BookId, sn.SeqId, sn.SeqName, s.SeqNumb, s.Level, s.Type
  FROM libseq s JOIN libseqname sn ON sn.SeqId = s.SeqId
 WHERE s.BookId IN (`, ids, `) ORDER BY s.BookId, s.Type, s.Level, sn.SeqName`)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query sequences batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bookID int64
		var s model.DBSequence
		if err := rows.Scan(&bookID, &s.ID, &s.Name, &s.Number, &s.Level, &s.Type); err != nil {
			return fmt.Errorf("scan sequence batch: %w", err)
		}
		src := out[bookID]
		src.Sequences = append(src.Sequences, s)
		out[bookID] = src
	}
	return rows.Err()
}

func (r *Repository) attachRatings(ctx context.Context, ids []int64, out map[int64]model.DatabaseSource) error {
	if ok, err := r.tableExists(ctx, "librate"); err != nil || !ok {
		return err
	}
	query, args := inQuery(`
SELECT BookId, ROUND(AVG(CAST(Rate AS UNSIGNED)), 0), COUNT(*), MIN(CAST(Rate AS UNSIGNED)), MAX(CAST(Rate AS UNSIGNED))
  FROM librate WHERE BookId IN (`, ids, `) GROUP BY BookId`)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query ratings batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bookID int64
		var avg sql.NullFloat64
		var count int64
		var min, max sql.NullInt64
		if err := rows.Scan(&bookID, &avg, &count, &min, &max); err != nil {
			return fmt.Errorf("scan rating batch: %w", err)
		}
		src := out[bookID]
		src.Rating = &model.DBRating{Average: avg.Float64, Count: count, Min: min.Int64, Max: max.Int64}
		out[bookID] = src
	}
	return rows.Err()
}

func (r *Repository) attachFilenames(ctx context.Context, ids []int64, out map[int64]model.DatabaseSource) error {
	if ok, err := r.tableExists(ctx, "libfilename"); err != nil || !ok {
		return err
	}
	query, args := inQuery(`SELECT BookId, FileName FROM libfilename WHERE BookId IN (`, ids, `) ORDER BY BookId, FileName`)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query filenames batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var bookID int64
		var name string
		if err := rows.Scan(&bookID, &name); err != nil {
			return fmt.Errorf("scan filename batch: %w", err)
		}
		src := out[bookID]
		src.Filenames = append(src.Filenames, name)
		out[bookID] = src
	}
	return rows.Err()
}

func (r *Repository) attachJoinedBooks(ctx context.Context, ids []int64, out map[int64]model.DatabaseSource) error {
	if ok, err := r.tableExists(ctx, "libjoinedbooks"); err != nil || !ok {
		return err
	}
	placeholders, args := inClause(ids)
	query := `SELECT Id, Time, BadId, GoodId, COALESCE(realId, 0) FROM libjoinedbooks WHERE BadId IN (` + placeholders + `) OR GoodId IN (` + placeholders + `) OR realId IN (` + placeholders + `) ORDER BY Time`
	args = append(append(args, args...), args...)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query joined books batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var jb model.DBJoinedBook
		var tm sql.NullTime
		if err := rows.Scan(&jb.ID, &tm, &jb.BadID, &jb.GoodID, &jb.RealID); err != nil {
			return fmt.Errorf("scan joined book batch: %w", err)
		}
		jb.Time = formatTime(tm)
		seen := make(map[int64]bool, 3)
		for _, id := range []int64{jb.BadID, jb.GoodID, jb.RealID} {
			if seen[id] {
				continue
			}
			seen[id] = true
			if _, ok := out[id]; ok {
				src := out[id]
				src.JoinedBooks = append(src.JoinedBooks, jb)
				out[id] = src
			}
		}
	}
	return rows.Err()
}

func (r *Repository) book(ctx context.Context, id int64) (*model.DBBook, bool, error) {
	if ok, err := r.tableExists(ctx, "libbook"); err != nil || !ok {
		return nil, false, err
	}
	replacedBy, err := r.columnExists(ctx, "libbook", "ReplacedBy")
	if err != nil {
		return nil, false, err
	}
	extra := ""
	if replacedBy {
		extra = ", ReplacedBy"
	}
	row := r.db.QueryRowContext(ctx, `
SELECT BookId, FileSize, Time, Title, Lang, SrcLang,
       FileType, Year, Deleted, FileAuthor, keywords, CAST(md5 AS CHAR),
       Modified`+extra+`
  FROM libbook WHERE BookId = ?`, id)

	var b model.DBBook
	var tm, modified sql.NullTime
	dest := []any{&b.BookID, &b.FileSize, &tm, &b.Title, &b.Lang,
		&b.SrcLang, &b.FileType, &b.Year, &b.Deleted, &b.FileAuthor,
		&b.Keywords, &b.MD5, &modified}
	if replacedBy {
		dest = append(dest, &b.ReplacedBy)
	}
	if err := row.Scan(dest...); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("query book %d: %w", id, err)
	}
	b.Time = formatTime(tm)
	b.Modified = formatTime(modified)
	b.MD5 = strings.TrimRight(b.MD5, "\x00")
	return &b, true, nil
}

func (r *Repository) contributors(ctx context.Context, id int64, table string, idColumn string) ([]model.Contributor, error) {
	if ok, err := r.tableExists(ctx, table); err != nil || !ok {
		return nil, err
	}
	if ok, err := r.tableExists(ctx, "libavtorname"); err != nil || !ok {
		return nil, err
	}
	useAliases, err := r.useAuthorAliases(ctx, table)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
SELECT n.AvtorId, n.FirstName, n.MiddleName, n.LastName, n.NickName, n.uid,
       n.Email, n.Homepage, n.Gender, n.MasterId, a.Pos
	  FROM %s
	 WHERE a.BookId = ?`, contributorFromClause(table, idColumn, useAliases))
	rows, err := r.db.QueryContext(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("query contributors from %s for book %d: %w", table, id, err)
	}
	defer rows.Close()
	var out []model.Contributor
	for rows.Next() {
		var c model.Contributor
		if err := rows.Scan(&c.ID, &c.FirstName, &c.MiddleName, &c.LastName, &c.NickName,
			&c.UID, &c.Email, &c.Homepage, &c.Gender, &c.MasterID, &c.Position); err != nil {
			return nil, fmt.Errorf("scan contributor: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repository) useAuthorAliases(ctx context.Context, table string) (bool, error) {
	if table != "libavtor" {
		return false, nil
	}
	return r.tableExists(ctx, "libavtoraliase")
}

func contributorFromClause(table string, idColumn string, useAliases bool) string {
	if useAliases {
		return fmt.Sprintf(
			"%s a LEFT JOIN libavtoraliase aa ON aa.BadId = a.%s JOIN libavtorname n ON n.AvtorId = COALESCE(aa.GoodId, a.%s)",
			table,
			idColumn,
			idColumn,
		)
	}
	return fmt.Sprintf("%s a JOIN libavtorname n ON n.AvtorId = a.%s", table, idColumn)
}

func (r *Repository) genres(ctx context.Context, id int64) ([]model.DBGenre, error) {
	if ok, err := r.tableExists(ctx, "libgenre"); err != nil || !ok {
		return nil, err
	}
	if ok, err := r.tableExists(ctx, "libgenrelist"); err != nil || !ok {
		return nil, err
	}
	query := `
SELECT gl.GenreId, gl.GenreCode, '', gl.GenreDesc, gl.GenreMeta
  FROM libgenre g JOIN libgenrelist gl ON gl.GenreId = g.GenreId
 WHERE g.BookId = ?`
	if ok, err := r.tableExists(ctx, "libgenretranslate"); err != nil {
		return nil, err
	} else if ok {
		query = `
SELECT gl.GenreId, gl.GenreCode, COALESCE(gt.trgGenreCode, ''), gl.GenreDesc, gl.GenreMeta
  FROM libgenre g JOIN libgenrelist gl ON gl.GenreId = g.GenreId
  LEFT JOIN libgenretranslate gt ON gt.srcGenreCode = gl.GenreCode
 WHERE g.BookId = ?`
	}
	rows, err := r.db.QueryContext(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("query genres for book %d: %w", id, err)
	}
	defer rows.Close()
	var out []model.DBGenre
	for rows.Next() {
		var g model.DBGenre
		if err := rows.Scan(&g.ID, &g.Code, &g.TranslatedCode, &g.Description, &g.Meta); err != nil {
			return nil, fmt.Errorf("scan genre: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *Repository) sequences(ctx context.Context, id int64) ([]model.DBSequence, error) {
	if ok, err := r.tableExists(ctx, "libseq"); err != nil || !ok {
		return nil, err
	}
	if ok, err := r.tableExists(ctx, "libseqname"); err != nil || !ok {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT sn.SeqId, sn.SeqName, s.SeqNumb, s.Level, s.Type
  FROM libseq s JOIN libseqname sn ON sn.SeqId = s.SeqId
 WHERE s.BookId = ? ORDER BY s.Type, s.Level, sn.SeqName`, id)
	if err != nil {
		return nil, fmt.Errorf("query sequences for book %d: %w", id, err)
	}
	defer rows.Close()
	var out []model.DBSequence
	for rows.Next() {
		var s model.DBSequence
		if err := rows.Scan(&s.ID, &s.Name, &s.Number, &s.Level, &s.Type); err != nil {
			return nil, fmt.Errorf("scan sequence: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repository) rating(ctx context.Context, id int64) (*model.DBRating, error) {
	if ok, err := r.tableExists(ctx, "librate"); err != nil || !ok {
		return nil, err
	}
	row := r.db.QueryRowContext(ctx, `
SELECT ROUND(AVG(CAST(Rate AS UNSIGNED)), 0), COUNT(*), MIN(CAST(Rate AS UNSIGNED)), MAX(CAST(Rate AS UNSIGNED))
  FROM librate WHERE BookId = ?`, id)
	var avg sql.NullFloat64
	var count int64
	var min, max sql.NullInt64
	if err := row.Scan(&avg, &count, &min, &max); err != nil {
		return nil, fmt.Errorf("query rating for book %d: %w", id, err)
	}
	if count == 0 {
		return nil, nil
	}
	return &model.DBRating{Average: avg.Float64, Count: count, Min: min.Int64, Max: max.Int64}, nil
}

func (r *Repository) filenames(ctx context.Context, id int64) ([]string, error) {
	if ok, err := r.tableExists(ctx, "libfilename"); err != nil || !ok {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, "SELECT FileName FROM libfilename WHERE BookId = ? ORDER BY FileName", id)
	if err != nil {
		return nil, fmt.Errorf("query filenames for book %d: %w", id, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan filename: %w", err)
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func (r *Repository) joinedBooks(ctx context.Context, id int64) ([]model.DBJoinedBook, error) {
	if ok, err := r.tableExists(ctx, "libjoinedbooks"); err != nil || !ok {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT Id, Time, BadId, GoodId, COALESCE(realId, 0)
  FROM libjoinedbooks WHERE BadId = ? OR GoodId = ? OR realId = ? ORDER BY Time`, id, id, id)
	if err != nil {
		return nil, fmt.Errorf("query joined books for book %d: %w", id, err)
	}
	defer rows.Close()
	var out []model.DBJoinedBook
	for rows.Next() {
		var jb model.DBJoinedBook
		var tm sql.NullTime
		if err := rows.Scan(&jb.ID, &tm, &jb.BadID, &jb.GoodID, &jb.RealID); err != nil {
			return nil, fmt.Errorf("scan joined book: %w", err)
		}
		jb.Time = formatTime(tm)
		out = append(out, jb)
	}
	return out, rows.Err()
}

func (r *Repository) tableExists(ctx context.Context, table string) (bool, error) {
	r.tmu.RLock()
	if exists, ok := r.tables[table]; ok {
		r.tmu.RUnlock()
		return exists, nil
	}
	r.tmu.RUnlock()

	var count int
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.tables
 WHERE table_schema = DATABASE() AND table_name = ?`, table).Scan(&count); err != nil {
		return false, fmt.Errorf("check table %q: %w", table, err)
	}
	r.tmu.Lock()
	defer r.tmu.Unlock()
	r.tables[table] = count > 0
	return r.tables[table], nil
}

func (r *Repository) columnExists(ctx context.Context, table string, column string) (bool, error) {
	r.tmu.RLock()
	if columns, ok := r.cols[table]; ok {
		if exists, ok := columns[column]; ok {
			r.tmu.RUnlock()
			return exists, nil
		}
	}
	r.tmu.RUnlock()

	var count int
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.columns
 WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, column).Scan(&count); err != nil {
		return false, fmt.Errorf("check column %q.%q: %w", table, column, err)
	}
	r.tmu.Lock()
	defer r.tmu.Unlock()
	if r.cols[table] == nil {
		r.cols[table] = make(map[string]bool)
	}
	r.cols[table][column] = count > 0
	return r.cols[table][column], nil
}

func formatTime(v sql.NullTime) string {
	if !v.Valid {
		return ""
	}
	return v.Time.Format(time.RFC3339)
}

func inQuery(prefix string, ids []int64, suffix string) (string, []any) {
	clause, args := inClause(ids)
	return prefix + clause + suffix, args
}

func inClause(ids []int64) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}
