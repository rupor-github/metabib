# FLibrary INPX Implementation Plan

This plan documents the agreed design for adding a new `flib-inpx` command that produces FLibrary-compatible INPX archives from merged metabib JSONL data.

The command is a sibling of the existing `mhl-inpx` command. It should consume the same merged JSONL parts and metadata sidecar, but target FLibrary's extended INPX importer instead of MyHomeLib/lib2inpx compatibility.

## Goals

- Add `metabib flib-inpx` for FLibrary-compatible INPX output.
- Use FLibrary extensions to preserve more metadata than classic MyHomeLib INPX.
- Emit no dummy records.
- Preserve multiple flat series links by writing repeated rows for the same physical book.
- Flatten FB2 hierarchical sequences in a configurable way.
- Keep `mhl-inpx` behavior intact.
- Keep implementation clear by splitting MyHomeLib and FLibrary generators into separate packages.

## References

- `docs/flibrary-inpx-format.md`: FLibrary INPX format notes.
- `docs/database-seq.md`: Flibusta `libseq` and sequence hierarchy notes.
- FLibrary source: `/home/rupor/projects/github.com/heimdallr/books`.
- Current generator: `inpx/inpx.go`.

## Verified FLibrary Behavior

FLibrary reads INPX as a zip archive. It recognizes:

- `*.inp`
- `collection.info`
- `version.info`
- `structure.info`

FLibrary uses `structure.info` to map fields. If `FOLDER` is present in `structure.info`, FLibrary uses it instead of deriving the archive name from the `.inp` member name.

FLibrary supports multiple series for one book by accepting repeated `.inp` rows with the same physical identity:

```text
FOLDER + FILE + "." + EXT
```

During import:

- The first row creates the book metadata.
- Every row can add one `SERIES`/`SERNO` relation before duplicate-book detection skips duplicate book insertion.
- Duplicate series links for the same book are ignored after the first occurrence.
- `Series_List.OrdNum` preserves first-seen series order.

FLibrary does not support hierarchical series structurally. `Series` and `Series_List` are flat tables.

FLibrary keyword parsing:

- Splits on punctuation including comma, semicolon, slash, dot, parentheses, and brackets.
- Also splits on `Inpx::LIST_SEPARATOR`, which is `:`.
- Therefore `flib-inpx` should emit colon-separated keywords for maximum FLibrary compatibility.

FLibrary schema declares sizes such as `SeriesTitle VARCHAR(80)`, `KeywordTitle VARCHAR(150)`, `Title VARCHAR(200)`, and `SourceLib VARCHAR(15)`, but SQLite does not enforce these lengths and the importer does not truncate input. `flib-inpx` should not apply MyHomeLib `quick_fix` truncation or INPX length limits.

## Package Refactor

Go package names cannot contain dashes. Use:

```text
mhlinpx/
flibinpx/
internal/inpxutil/
```

Plan:

- Rename current `inpx` package to `mhlinpx`.
- Keep CLI command name `mhl-inpx`.
- Add new `flibinpx` package for FLibrary output.
- Move shared input and zip utilities to `internal/inpxutil`.

Shared utilities should include:

- Metadata sidecar discovery.
- JSONL part discovery.
- Merge metadata reading.
- Record loading into archive buckets.
- INPX output path construction.
- Zip text writer.
- Template rendering for `collection.info` and `version.info`.
- `Stats` and archive stats types, if useful.
- Field separator constant.
- Common cleansing and date helpers.

## CLI

Add command:

```sh
metabib flib-inpx --input all --output flibusta
```

Flags:

```text
--input PREFIX, -i PREFIX
--output PREFIX, -o PREFIX
--prefer-fb2 MODE
--sequence MODE
--fb2-flatten MODE
--source-lib VALUE
```

Defaults:

```text
--prefer-fb2 complement
--sequence author
--fb2-flatten all
--source-lib <merge metadata library name>
```

Do not expose config-owned settings as command-line flags. `sequence_dedup` and
`fb2_path_separator` are intentionally configuration-only.

## Config

Extend the `inpx` config with FLibrary-specific defaults:

```yaml
inpx:
  flibrary:
    sequence_dedup: case-insensitive
    fb2_path_separator: " / "
```

Recommended Go structs:

```go
type INPXConfig struct {
    QuickFix        bool               `yaml:"quick_fix"`
    CommentTemplate string             `yaml:"comment_template"`
    VersionTemplate string             `yaml:"version_template"`
    Limits          INPXLimits         `yaml:"limits"`
    FLibrary        FLibraryINPXConfig `yaml:"flibrary"`
}

type FLibraryINPXConfig struct {
    SequenceDedup    string `yaml:"sequence_dedup" validate:"oneof=case-insensitive case-sensitive"`
    FB2PathSeparator string `yaml:"fb2_path_separator" validate:"required"`
}
```

Use hyphenated values consistently for config values:

```text
case-insensitive
case-sensitive
```

Update `config/config.yaml.tmpl` with the new `inpx.flibrary` block and comments explaining:

- `sequence_dedup`: controls case-sensitive vs case-insensitive FLibrary sequence deduplication.
- `fb2_path_separator`: joins nested FB2 sequence names when `--fb2-flatten path` or `path-leaf` is used.

Keep the existing `quick_fix` and `limits` comments MyHomeLib-specific. `flib-inpx` should not use those limits unless future FLibrary behavior requires it.

## Output Format

Always write `structure.info` with:

```text
AUTHOR;GENRE;TITLE;SERIES;SERNO;FILE;SIZE;LIBID;DEL;EXT;DATE;INSNO;FOLDER;LANG;LIBRATE;KEYWORDS;YEAR;SOURCELIB;
```

Each row should contain:

- `AUTHOR`: selected DB/FB2 authors.
- `GENRE`: DB genres, fallback FB2 genres.
- `TITLE`: DB title, fallback FB2 title.
- `SERIES`: one flattened series title for this row.
- `SERNO`: sequence number for `SERIES`.
- `FILE`: file base name without extension.
- `SIZE`: DB file size, fallback archive uncompressed size.
- `LIBID`: `BookID`.
- `DEL`: DB deletion flag.
- `EXT`: extension without dot.
- `DATE`: DB update date, fallback archive modified date.
- `INSNO`: emitted row index in the `.inp` member.
- `FOLDER`: archive basename, preferably `filepath.Base(archive.Meta.Path)`, fallback `archive.Meta.Name`.
- `LANG`: DB language, fallback FB2 language.
- `LIBRATE`: DB average rating.
- `KEYWORDS`: colon-separated keyword values.
- `YEAR`: DB year, fallback FB2 publish-info year.
- `SOURCELIB`: `--source-lib` value, otherwise merge metadata library name.

No dummy rows should be emitted.

## Record Selection

`flib-inpx` is FB2-only like `mhl-inpx`.

For each archive:

- Iterate actual merged records only.
- Sort by archive index for stable output.
- Skip ignored indexes.
- Skip non-FB2 records.
- Skip records with neither DB nor FB2 title metadata.
- Emit one row if no sequence is selected.
- Emit one row per selected sequence if sequences exist.
- Repeat complete book metadata on every repeated row.
- Increment `INSNO` per emitted row.

## Metadata Precedence

Keep current `mhl-inpx` behavior outside multi-series support unless explicitly changed.

Authors:

- `--prefer-fb2 replace`: use FB2 authors if present.
- Otherwise use DB authors if present.
- If DB authors are absent, use FB2 authors if present.
- If still absent, emit `неизвестный,автор,:`.
- Do not merge DB and FB2 author lists.

Genres:

- Use DB genres if present.
- Otherwise use FB2 genres if present.
- Otherwise emit `other:`.

Title:

- Use DB title.
- Fallback to FB2 title.

Size:

- Use DB file size.
- Fallback to archive uncompressed size.

Date:

- Use DB book time date.
- Fallback to archive modified date.

Language:

- Use DB language.
- Fallback to FB2 language.

Keywords:

- Use DB keywords.
- Fallback to FB2 keywords.
- Normalize output to colon-separated keyword values.

Year:

- Use DB year.
- Fallback to FB2 publish-info year.

Source library:

- Use `--source-lib` when set.
- Otherwise use merge metadata library name.

## Sequence Source Modes

Reuse `--prefer-fb2` for sequence source selection.

Modes:

```text
ignore
complement
merge
replace
```

Sequence behavior:

- `ignore`: use selected DB sequences only, no FB2 sequences.
- `complement`: use selected DB sequences if any; otherwise selected FB2 sequences.
- `merge`: use selected DB sequences first, then selected FB2 sequences.
- `replace`: use selected FB2 sequences if any; otherwise selected DB sequences.

Default is `complement` for compatibility with `mhl-inpx` behavior.

## Sequence Selection Modes

Use `--sequence MODE`:

```text
author
publisher
all
ignore
```

Meaning:

- `author`: default. Include DB `Type = 0` and FB2 title-info sequences.
- `publisher`: include DB `Type = 1` and FB2 publish-info sequences.
- `all`: include both author/title-info and publisher/publish-info sequences.
- `ignore`: ignore DB sequences. FB2 sequences can still be used according to `--prefer-fb2`.

Publisher sequences are excluded by default and included only when explicitly requested via `publisher` or `all`.

## DB Sequence Handling

Flibusta DB sequences are already flat for FLibrary purposes.

Rules:

- Treat each selected `libseq` row as an independent FLibrary series.
- Do not infer hierarchy from DB `Level`.
- Always emit all selected DB rows before deduplication.
- Preserve `SeqNumb` as `SERNO`.

Suggested sorting:

- `author`: `Type ASC, Level ASC, Name ASC` after filtering to `Type = 0`.
- `publisher`: `Type DESC, Level ASC, Name ASC` after filtering to `Type = 1`.
- `all`: `Type ASC, Level ASC, Name ASC`.

## FB2 Sequence Flattening

FB2 sequences can be hierarchical through nested `<sequence>` elements.

Use `--fb2-flatten MODE`:

```text
all
leaf
path
path-leaf
```

Default is `all`.

Meaning:

- `all`: emit every FB2 sequence node independently.
- `leaf`: emit only leaf sequence nodes.
- `path`: emit one combined path per leaf, for example `Universe / Cycle / Subcycle`.
- `path-leaf`: emit combined path plus leaf sequence.

Use configured path separator from:

```yaml
inpx:
  flibrary:
    fb2_path_separator: " / "
```

When emitting path series, use the leaf node number as `SERNO`.

## Sequence Deduplication

Deduplicate selected sequences after DB/FB2 combination and FB2 flattening.

Default mode is case-insensitive.

Modes:

```text
case-insensitive
case-sensitive
```

Rules:

- Deduplicate by sequence name only.
- Ignore sequence number for deduplication.
- Trim surrounding whitespace before comparing.
- In `case-insensitive` mode, compare folded/lowercased names.
- First occurrence wins.
- Drop later duplicates even if `SERNO` differs.
- Log dropped duplicates at debug level with enough context to identify kept and discarded values.

Example in `case-insensitive` mode:

```text
"Cycle" == "cycle" == " CYCLE "
```

## Implementation Steps

1. Rename `inpx` package to `mhlinpx`.
2. Update CLI imports, tests, and references for `mhl-inpx`.
3. Extract shared input/zip/template helpers to `internal/inpxutil`.
4. Add `FLibraryINPXConfig` under `inpx` config.
5. Update `config/config.yaml.tmpl` comments and defaults for `inpx.flibrary`.
6. Add `flibinpx.Options` and parsers for CLI/config modes.
7. Add `flib-inpx` command in `cmd/metabib/main.go`.
8. Implement FLibrary record construction and row serialization.
9. Implement DB sequence filtering and sorting.
10. Implement FB2 sequence walking and flattening modes.
11. Implement DB/FB2 sequence combination via `--prefer-fb2`.
12. Implement sequence deduplication with debug logging.
13. Implement no-dummy archive writer that iterates only real records.
14. Write `structure.info`, `collection.info`, and `version.info`.
15. Add unit and integration-style tests.
16. Update README and format docs.

## Documentation Updates

Update `README.md` with:

- A new `flib-inpx` usage section next to `mhl-inpx`.
- Required `--input` and `--output` behavior.
- Explanation that `flib-inpx` consumes merged JSONL and metadata sidecar, not SQL dumps or archives directly.
- Explanation that it is FB2-only and emits no dummy records.
- Explanation of FLibrary-specific repeated rows for multiple series.
- CLI options and defaults: `--prefer-fb2`, `--sequence`, `--fb2-flatten`, and `--source-lib`.
- Config-only `inpx.flibrary` settings: `sequence_dedup` and `fb2_path_separator`.
- Explanation of the `inpx.flibrary` config block.

Update `config/config.yaml.tmpl` with comments for the new FLibrary settings, so `metabib dumpconfig --default` documents them.

Update `docs/flibrary-inpx-format.md` after implementation with the exact producer choices made by `flib-inpx`, including field order, sequence flattening, and deduplication defaults.

## Tests

Add tests for:

- Existing `mhl-inpx` behavior after package rename.
- `flib-inpx` `structure.info` contents.
- FLibrary row field count and order.
- No dummy rows when archive entries are missing.
- FB2-only filtering.
- Multiple sequences produce repeated rows with identical `FOLDER + FILE + EXT`.
- `INSNO` increments per emitted row.
- `FOLDER`, `YEAR`, and `SOURCELIB` are populated.
- Keyword normalization to colon-separated values.
- `--prefer-fb2 ignore`, `complement`, `merge`, and `replace` for sequences.
- `--sequence author`, `publisher`, `all`, and `ignore`.
- FB2 flattening modes `all`, `leaf`, `path`, and `path-leaf`.
- Configured FB2 path separator.
- Sequence dedup `case-insensitive` and `case-sensitive`.
- Duplicate sequence names with different numbers collapse in default mode and log debug.

## Validation

After Go edits:

```sh
GOEXPERIMENT=jsonv2 GOFLAGS=-mod=mod goimports-reviser -format -set-alias -company-prefixes github.com/rupor-github <changed .go files>
GOEXPERIMENT=jsonv2 gopls check -severity=hint <changed .go files>
GOEXPERIMENT=jsonv2 go test -mod=mod ./...
GOEXPERIMENT=jsonv2 GOFLAGS=-mod=mod go tool staticcheck -f stylish -tests=true ./...
```

Use `go tool task` for the default developer build when appropriate.
