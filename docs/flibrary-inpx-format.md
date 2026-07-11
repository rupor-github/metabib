# FLibrary INPX Format Notes

This document describes the INPX variant consumed by FLibrary, with emphasis on producing compatible INPX files from external tools.

The implementation references are mainly `src/home/inpx/inpx.cpp`, `src/home/inpx/types.h`, `src/ext/foundation/Constant.h`, and `src/ext/foundation/util/Fb2InpxParser.cpp`.

## Container

An INPX file is a zip archive. FLibrary recognizes these members:

- `*.inp`: book index records.
- `collection.info`: optional collection display name. The add-collection dialog reads the first line.
- `version.info`: optional version/date marker.
- `structure.info`: optional field layout description for all `.inp` records in the archive.

Other members are ignored by the core INPX parser.

## Record Encoding

`.inp` records are UTF-8 text lines.

Fields inside one `.inp` line are separated by byte `0x04`, exposed in code as `Fb2InpxParser::FIELDS_SEPARATOR`.

In examples below, `<FS>` means the single `0x04` field separator byte.

```text
AUTHOR<FS>GENRE<FS>TITLE<FS>SERI...<FS>...
```

The value-list separator inside fields such as `AUTHOR`, `GENRE`, and `KEYWORDS` is `:`. This is different from `structure.info`, where field names are separated by `;`.

Author name parts are separated by `,`:

```text
Last,First,Middle:OtherLast,OtherFirst,OtherMiddle:
```

## Field Layout

If `structure.info` exists, FLibrary reads it and uses that exact field order. Field names in `structure.info` are separated by `;`:

```text
AUTHOR;GENRE;TITLE;SERIES;SERNO;FILE;SIZE;LIBID;DEL;EXT;DATE;INSNO;FOLDER;LANG;LIBRATE;KEYWORDS;YEAR;SOURCELIB;
```

If `structure.info` is absent, FLibrary falls back to the default classic-compatible layout:

```text
AUTHOR;GENRE;TITLE;SERIES;SERNO;FILE;SIZE;LIBID;DEL;EXT;DATE;LANG;LIBRATE;KEYWORDS;
```

Known fields are:

- `AUTHOR`: `:`-separated list of authors. Each author is `Last,First,Middle`.
- `GENRE`: `:`-separated list of FB2 genre codes or aliases.
- `TITLE`: book title.
- `SERIES`: one series title for this record.
- `SERNO`: sequence number for `SERIES`. Non-positive or non-numeric values are treated as empty.
- `FILE`: file base name without extension.
- `SIZE`: stored as `Books.BookSize`.
- `LIBID`: stored as `Books.LibID`.
- `DEL`: deletion flag, usually `0` or `1`.
- `EXT`: file extension without leading dot.
- `DATE`: update/add date. FLibrary uses year and month from this value for update navigation.
- `INSNO`: accepted and ignored.
- `FOLDER`: archive file containing the book, for example `fb2-123456-123999.zip`.
- `LANG`: language code, later normalized by FLibrary.
- `LIBRATE`: library rating.
- `KEYWORDS`: keywords; FLibrary normalizes and splits this value.
- `YEAR`: publication year, stored as `Books.Year`.
- `SOURCELIB`: source library marker, stored as `Books.SourceLib`.

Unknown fields are ignored. Unknown fields other than `INSNO` are logged as warnings.

`FILE` and `EXT` are parsed without trimming leading spaces. Other fields skip leading Unicode space separators.

## Folder Resolution

The physical book identity used by FLibrary is:

```text
FOLDER + FILE + "." + EXT
```

If a record has an empty `FOLDER`, FLibrary derives it from the `.inp` member name:

- For member `fb2-123.inp`, it looks for an archive named `fb2-123.*` in the collection folder.
- If such an archive exists, the first matching file name is used.
- If no archive exists, the fallback is `fb2-123.<default_archive_type>`.

For externally generated extended INPX files, prefer writing `FOLDER` explicitly and including it in `structure.info`.

## Multiple Series

The `SERIES` field is not a list. One `.inp` row has at most one `SERIES` and one `SERNO`.

FLibrary supports multiple flat series links for the same book by accepting repeated rows that point to the same physical file:

```text
same FOLDER + same FILE + same EXT + different SERIES/SERNO
```

Example shown with `<FS>` as the `0x04` separator:

```text
Doe,John,:<FS>sf:<FS>Example Book<FS>Main Cycle<FS>1<FS>example<FS>12345<FS>100<FS>0<FS>fb2<FS>2026-01-01<FS>1<FS>books.zip<FS>en<FS>0<FS><FS>
Doe,John,:<FS>sf:<FS>Example Book<FS>Side Collection<FS>7<FS>example<FS>12345<FS>100<FS>0<FS>fb2<FS>2026-01-01<FS>2<FS>books.zip<FS>en<FS>0<FS><FS>
```

With matching `structure.info`:

```text
AUTHOR;GENRE;TITLE;SERIES;SERNO;FILE;SIZE;LIBID;DEL;EXT;DATE;INSNO;FOLDER;LANG;LIBRATE;KEYWORDS;
```

On import:

- The first row creates the book.
- Each row adds its `SERIES`/`SERNO` relation before duplicate-book detection stops further book insertion.
- Duplicate rows do not merge other metadata such as authors, genres, title, language, keywords, year, or source library.
- If the same series appears more than once for the same book, only the first relation is stored.
- `OrdNum` in `Series_List` preserves the order of unique series relations as they were parsed.

For best results, put complete desired metadata on the first row and repeat it on additional rows for compatibility with other readers. FLibrary will use the repeated rows mainly for extra series links.

## Hierarchical Series

FLibrary has no structural support for nested series.

The database has a flat `Series` table and a flat `Series_List(BookID, SeriesID, SeqNumber, OrdNum)` table. There is no parent series column.

The direct FB2 parser also stores only one sequence:

```cpp
m_data.series    = attributes.GetAttribute("name").trimmed();
m_data.seqNumber = GetSeqNumber(attributes.GetAttribute("number"));
```

External generators that need to preserve FB2 nested `<sequence>` information must flatten it, for example by:

- Emitting one combined title such as `Universe / Cycle / Trilogy`.
- Emitting several flat series rows for the same book, one per hierarchy level.

The second option makes the book appear in several independent FLibrary series. It does not preserve parent-child relationships.

## Genres

`GENRE` is multi-valued and uses `:` as the value-list separator:

```text
sf_history:adv_geo:prose_classic:
```

During import FLibrary:

- Resolves genre codes and aliases from `genres.json`.
- Removes duplicate genre links per book.
- Creates unknown genres under `unknown_root` when a value cannot be resolved.
- Stores links in `Genre_List` with an order number.

Trailing `:` is customary and produced by FLibrary, but parsing does not require the last value to have a trailing separator.

## Update Behavior

FLibrary stores parsed INPX members in table `Inpx(Folder, File, Hash)`, where:

- `Folder` is the INPX file name.
- `File` is the `.inp` member name inside that INPX.
- `Hash` is an MD5 hash of the `.inp` member content.

Automatic collection update is append-oriented:

- Newly added `.inp` members are detected and parsed.
- Changes to already known `.inp` members are noticed as old data changes, but they are not fully re-parsed as an in-place replacement.
- When old `.inp` members change, FLibrary recommends recreating the collection.

For an external updater, prefer adding new `.inp` members for incremental updates. If existing records must be changed or removed, expect users to rebuild/recreate the collection database.

## Producer Checklist

- Write INPX as a zip archive.
- Encode `.inp` members as UTF-8 text.
- Separate fields with byte `0x04`.
- Include `structure.info` when using `FOLDER`, `YEAR`, `SOURCELIB`, or any non-default field order.
- Use `:` for multi-value fields such as `AUTHOR`, `GENRE`, and `KEYWORDS`.
- Use `,` between author name parts.
- Store `EXT` without leading dot.
- Prefer explicit `FOLDER` values.
- Encode multiple series as repeated rows for the same `FOLDER + FILE + EXT`.
- Put authoritative non-series metadata in the first row for a physical book.
- Add new `.inp` members for incremental updates instead of rewriting old ones.

## metabib flib-inpx Choices

`metabib flib-inpx` consumes merged metabib JSONL parts and the merge metadata
sidecar. It does not read SQL dumps or FB2 archives directly.

The command writes this `structure.info` exactly:

```text
AUTHOR;GENRE;TITLE;SERIES;SERNO;FILE;SIZE;LIBID;DEL;EXT;DATE;INSNO;FOLDER;LANG;LIBRATE;KEYWORDS;YEAR;SOURCELIB;
```

Producer behavior:

- Only FB2 records are emitted.
- Dummy records are not emitted.
- `FOLDER` is the archive basename from merge metadata.
- `INSNO` is the emitted row index inside each `.inp` member.
- `SOURCELIB` defaults to the merge metadata library name and can be overridden
  with `--source-lib`.
- Keywords are normalized to colon-separated values.
- MyHomeLib `quick_fix` limits are not applied.

Sequence behavior:

- `--sequence author` selects DB `Type = 0` and FB2 title-info sequences.
- `--sequence publisher` selects DB `Type = 1` and FB2 publish-info sequences.
- `--sequence all` selects both author/title-info and publisher/publish-info
  sequences.
- `--sequence ignore` ignores DB sequences; FB2 sequences can still be used
  according to `--prefer-fb2`.
- `--prefer-fb2 complement` uses DB sequences when present and otherwise FB2
  sequences.
- `--prefer-fb2 merge` emits DB sequences first, then FB2 sequences.
- `--prefer-fb2 replace` uses FB2 sequences when present and otherwise DB
  sequences.
- `--prefer-fb2 ignore` uses DB sequences only.

FB2 nested sequences are flattened with `--fb2-flatten all`, `leaf`, `path`, or
`path-leaf`. Path modes join names with `inpx.flibrary.fb2_path_separator`, which
defaults to `" / "`.

Duplicate sequences are removed by trimmed sequence name only. The default
deduplication mode is `case-insensitive`; `case-sensitive` can be selected with
`inpx.flibrary.sequence_dedup`.
