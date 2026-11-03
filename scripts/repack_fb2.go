// Repack an inclusive FB2 range, copying all other compressed entries verbatim.
// Usage: go run scripts/repack_fb2.go --input old.zip --books build/books \
// --first 893518 --last 893588 --output repaired.zip [--dry-run]
package main

import (
	"archive/zip"
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func main() {
	if err := runRepack(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func runRepack(args []string) (err error) {
	flags := flag.NewFlagSet("repack-fb2", flag.ContinueOnError)
	input := flags.String("input", "", "original ZIP (never modified)")
	books := flags.String("books", "", "directory containing replacement <book_id>.fb2 files")
	output := flags.String("output", "", "new ZIP path; must not already exist")
	first := flags.Uint64("first", 0, "first book ID, inclusive (required)")
	last := flags.Uint64("last", 0, "last book ID, inclusive (required)")
	dryRun := flags.Bool("dry-run", false, "validate inputs and show counts without writing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *input == "" || *books == "" || *output == "" || *first == 0 || *last < *first {
		return errors.New("require --input, --books, --output, and 1 <= --first <= --last")
	}
	if _, err := os.Lstat(*output); err == nil {
		return fmt.Errorf("output already exists: %s", *output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect output: %w", err)
	}

	source, err := zip.OpenReader(*input)
	if err != nil {
		return fmt.Errorf("open source ZIP: %w", err)
	}
	defer func() { err = errors.Join(err, source.Close()) }()

	ids := make([]uint64, len(source.File))
	removed := 0
	for i, file := range source.File {
		id, err := fb2BookID(file.Name)
		if err != nil {
			return err
		}
		if i > 0 && id <= ids[i-1] {
			return errors.New("source entries must be in strictly increasing book ID order")
		}
		ids[i] = id
		if id >= *first && id <= *last {
			removed++
		}
	}
	if removed == 0 {
		return errors.New("source ZIP has no entries in the requested range")
	}
	replacements, err := replacementBooks(*books, *first, *last)
	if err != nil {
		return err
	}
	fmt.Printf("Remove %d old entries; insert %d downloaded books; raw-copy %d unchanged entries.\n",
		removed, len(replacements), len(source.File)-removed)
	fmt.Printf("Output: %s\n", *output)
	if *dryRun {
		return nil
	}
	if err := publishRepacked(*output, &source.Reader, ids, replacements, *first, *last); err != nil {
		return err
	}
	fmt.Println("Done. Original ZIP unchanged; only replacement books were compressed.")
	return nil
}

type replacementBook struct {
	id   uint64
	path string
}

func fb2BookID(name string) (uint64, error) {
	stem, ok := strings.CutSuffix(name, ".fb2")
	if !ok || stem == "" || strings.ContainsAny(stem, "/\\") {
		return 0, fmt.Errorf("expected a flat <book_id>.fb2 filename: %q", name)
	}
	for _, char := range stem {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("invalid book ID in filename: %q", name)
		}
	}
	id, err := strconv.ParseUint(stem, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse book ID in %q: %w", name, err)
	}
	if id == 0 {
		return 0, fmt.Errorf("book ID must be positive: %q", name)
	}
	return id, nil
}

func replacementBooks(dir string, first, last uint64) ([]replacementBook, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read replacement directory: %w", err)
	}
	var books []replacementBook
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".fb2") {
			continue
		}
		id, err := fb2BookID(entry.Name())
		if err != nil {
			return nil, err
		}
		if id < first || id > last {
			return nil, fmt.Errorf("replacement %s is outside the requested range", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect replacement %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return nil, fmt.Errorf("replacement must be a nonempty regular file: %s", entry.Name())
		}
		books = append(books, replacementBook{id: id, path: filepath.Join(dir, entry.Name())})
	}
	if len(books) == 0 {
		return nil, errors.New("no replacement FB2 files found")
	}
	slices.SortFunc(books, func(a, b replacementBook) int { return cmp.Compare(a.id, b.id) })
	for i := 1; i < len(books); i++ {
		if books[i].id == books[i-1].id {
			return nil, fmt.Errorf("duplicate replacement book ID: %d", books[i].id)
		}
	}
	return books, nil
}

func publishRepacked(output string, source *zip.Reader, ids []uint64, books []replacementBook, first, last uint64) (err error) {
	temporary, err := os.CreateTemp(filepath.Dir(output), ".repack-fb2-*.zip")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	defer func() { err = errors.Join(err, os.Remove(temporary.Name())) }()
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, temporary.Close())
		}
	}()
	if err := writeRepacked(temporary, source, ids, books, first, last); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync output ZIP: %w", err)
	}
	closed = true
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close output ZIP: %w", err)
	}
	// A same-filesystem hard link publishes atomically without overwriting anything.
	if err := os.Link(temporary.Name(), output); err != nil {
		return fmt.Errorf("publish output ZIP (destination must not exist): %w", err)
	}
	return nil
}

func writeRepacked(dst io.Writer, source *zip.Reader, ids []uint64, books []replacementBook, first, last uint64) (err error) {
	writer := zip.NewWriter(dst)
	defer func() { err = errors.Join(err, writer.Close()) }()
	if err := writer.SetComment(source.Comment); err != nil {
		return fmt.Errorf("copy ZIP comment: %w", err)
	}
	inserted := false
	for i, file := range source.File {
		if !inserted && ids[i] >= first {
			for _, book := range books {
				if err := writeReplacement(writer, book); err != nil {
					return err
				}
			}
			inserted = true
		}
		if ids[i] >= first && ids[i] <= last {
			continue
		}
		// Copy bypasses decompression and recompression; compressed bytes stay identical.
		if err := writer.Copy(file); err != nil {
			return fmt.Errorf("raw-copy %s: %w", file.Name, err)
		}
	}
	return nil
}

func writeReplacement(writer *zip.Writer, book replacementBook) (err error) {
	file, err := os.Open(book.path)
	if err != nil {
		return fmt.Errorf("open replacement: %w", err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat replacement: %w", err)
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("replacement ZIP header: %w", err)
	}
	header.Name = fmt.Sprintf("%d.fb2", book.id)
	header.Method = zip.Deflate
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create replacement ZIP entry: %w", err)
	}
	if _, err := io.Copy(entry, file); err != nil {
		return fmt.Errorf("compress replacement %s: %w", book.path, err)
	}
	return nil
}
