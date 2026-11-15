# INPX generation

[User guide](guide.md) · [Dataset reference](datasets.md)

All INPX generators read a **merged dataset**, not SQL dumps, cache records, or
book files. You can export several reader-specific catalogs from one dataset
without repeating extraction.

## Contents

- [Choose a generator](#choose-a-generator)
- [MyHomeLib: mhl-inpx](#myhomelib-mhl-inpx)
- [FLibrary: flib-inpx](#flibrary-flib-inpx)
- [Filtered catalogs: inpx](#filtered-catalogs-inpx)
- [Metadata source preferences](#metadata-source-preferences)
- [Series selection](#series-selection)
- [Filter and split templates](#filter-and-split-templates)
- [Additional FLibrary artifacts](#additional-flibrary-artifacts)
- [Author disambiguation](#author-disambiguation)
- [Language normalization](#language-normalization)
- [Output details and compatibility](#output-details-and-compatibility)

## Choose a generator

| Goal | Command | Default content | Database-only input |
| --- | --- | --- | --- |
| Historical MyHomeLib layout | `mhl-inpx` | `fb2` | Supported |
| FLibrary layout with multiple series links | `flib-inpx` | `fb2` | Supported |
| Archive-backed filtering and splitting | `inpx` | `all` | Rejected |

Every generator requires `--input` (`-i`) and `--output` (`-o`):

```sh
metabib mhl-inpx --input library --output flibusta
```

Input can be an exact dataset path or a prefix resolving to exactly one of
`.jsonl`, `.jsonl.zst`, `.jsonl.gz`, or `.jsonl.zip`.
The output prefix gets a date suffix: for example,
`flibusta_20260603.inpx` when that is the dataset's dump date.

### Content selection

`--content fb2`, `usr`, or `all` selects FB2, non-FB2, or both.
Archive records use the **logical artifact name**, not the database's file type.
An opaque `.zip` inspected as PDF is selected as non-FB2; its physical occurrence
still points to the `.zip` member.

With nested inspection disabled, opaque containers keep their container
extension. Archive selection does not fall back to database `file_type`.
Database-only records do use `file_type`.

## MyHomeLib: mhl-inpx

```sh
metabib mhl-inpx --input library --output flibusta-mhl
```

| Option | Values | Default |
| --- | --- | --- |
| `--content` | `fb2`, `usr`, `all` | `fb2` |
| `--format` | `2x`, `ruks` | `2x` |
| `--sequence` | `author`, `publisher`, `ignore` | `author` |
| `--prefer-fb2` | `ignore`, `complement`, `merge`, `replace` | `complement` |

`ruks` appends MD5 and replacement fields when available.

MHL writes one row per accepted dataset record, with just one
`SERIES`/`SERNO` pair. It does not flatten nested FB2 series or emit extra rows
for multiple series. When archive records lack a usable title, it writes dummy
rows rather than omitting them.

For a dataset with no archives, records go into `online.inp`. When the header
contains archives, `online.inp` is not created and archive-less records are ignored.

## FLibrary: flib-inpx

```sh
metabib flib-inpx --input library --output flibusta-flib --additional
```

| Option | Values / purpose | Default |
| --- | --- | --- |
| `--content` | `fb2`, `usr`, `all` | `fb2` |
| `--sequence` | `author`, `publisher`, `all`, `ignore` | `author` |
| `--prefer-fb2` | `ignore`, `complement`, `merge`, `replace` | `complement` |
| `--fb2-flatten` | `all`, `leaf`, `path`, `path-leaf` | `all` |
| `--source-lib` | `SOURCELIB` value | Dataset library name |
| `--additional` | Write supported annotation/compilation archives. | Off |

FLibrary output always includes `structure.info` with extensions such as
`FOLDER`, `YEAR`, and `SOURCELIB`. It emits no dummy records.
Database-only input follows the same `online.inp` rule as MHL.

A book with several selected series produces several rows with the same
`FOLDER + FILE + EXT`, differing only in series fields. FLibrary imports them
as multiple series links for one physical book. Without selected series, a
book still gets one row with empty series fields.

## Filtered catalogs: inpx

Keep only Russian-language books:

```sh
metabib inpx --input library --output ru --where '{{eq .Lang "ru"}}'
```

Split accepted books into language-named `.inp` members inside one INPX:

```sh
metabib inpx --input library --output by-lang --split-by '{{default "unknown" .Lang}}'
```

`inpx` requires archive-backed input and writes explicit `FOLDER` and `INSNO`
locators. It rejects database-only datasets and skips archive-less records in
mixed datasets. Filtered records are omitted, not replaced by dummy rows.

| Option | Purpose |
| --- | --- |
| `--content` | `fb2`, `usr`, or `all`; default `all`. |
| `--where TEMPLATE` | Keep or drop each rendered row. |
| `--where-file FILE` | Load the filter from a file instead; exclusive with `--where`. |
| `--split-by TEMPLATE` | Choose the `.inp` member for each accepted book. |
| `--split-by-file FILE` | Load the split template from a file instead; exclusive with `--split-by`. |
| `--prefer-fb2`, `--sequence`, `--fb2-flatten` | Same choices and defaults as `flib-inpx`. |
| `--additional` | Write FLibrary annotation/compilation artifacts for accepted books. |

Without `--split-by`, accepted rows go to `books.inp`.

## Metadata source preferences

Titles, genres, language, dates, keywords, file size, deletion state, and rating
prefer database claims and fall back to FB2/archive claims when missing.
`--prefer-fb2` controls authors, series, and additional annotations—not a blanket
replacement of every metadata field.

### Authors

| `--prefer-fb2` | Author selection |
| --- | --- |
| `ignore` | DB authors; FB2 authors only when no DB record exists. |
| `complement` (default) | DB authors; FB2 authors only when no DB authors are selected. |
| `merge` | DB authors; does **not** concatenate DB and FB2 author lists. |
| `replace` | FB2 authors if present, otherwise DB authors. |

With a present DB record but no selected authors, the standard unknown-author
value is `неизвестный,автор,:`, unless `replace` has FB2 authors to use.
Database disambiguation applies only to selected DB authors.

Replacement-quality checks treat corrupted claims as absent. In particular,
FB2 people are ignored for replacement only when first, middle, and last names
are all non-renderable, leaving usable DB metadata available.
If no usable title remains, MHL writes a dummy archive row; the other generators
omit the record.

### Series preferences

These preferences apply after selecting the database series class:

| `--prefer-fb2` | MHL: one series | FLibrary / generic `inpx`: series list |
| --- | --- | --- |
| `ignore` | DB only. | DB only. |
| `complement` | DB if present, else first FB2 title series. | Selected DB series if any, else FB2 series. |
| `merge` | First FB2 title series if present, else DB. | DB followed by FB2, then deduplicated. |
| `replace` | First FB2 title series if present, else DB. | FB2 if present, else DB. |

**To suppress all series**, use `--sequence ignore --prefer-fb2 ignore`.
`--sequence ignore` alone removes DB series but can still allow FB2 title series.

## Series selection

Database type `0` means author series; type `1` means publisher series.

- In `flib-inpx` and `inpx`, `--sequence author`, `publisher`, or `all` selects
  the corresponding classes.
- In `mhl-inpx`, `author` or `publisher` is the preferred class when choosing
  a single DB series; `all` is unsupported.
- FB2 `title-info/sequence` is treated as author series.
  FB2 `publish-info/sequence` is treated as publisher series in the FLibrary
  generators.
- With `--sequence ignore`, FB2 title series can still participate in
  `complement`, `merge`, or `replace` modes.

### Flatten nested FB2 series

Only `flib-inpx` and `inpx` support `--fb2-flatten`.
For a nested series `Universe > Cycle #2`:

| Mode | Flat output |
| --- | --- |
| `all` | `Universe`, then `Cycle #2` |
| `leaf` | `Cycle #2` |
| `path` | `Universe / Cycle #2` |
| `path-leaf` | `Universe / Cycle #2`, then `Cycle #2` |

Configuration controls deduplication and the path separator:

```yaml
inpx:
  flibrary:
    sequence_dedup: case-insensitive
    fb2_path_separator: " / "
```

Deduplication runs after source selection and flattening. The first matching
name wins; in `merge` mode, DB series come first. Use `case-sensitive` to
distinguish names by case.

For example:

```sh
metabib flib-inpx --input library --output all-series --sequence all --prefer-fb2 merge --fb2-flatten path
metabib flib-inpx --input library --output fb2-series --sequence author --prefer-fb2 replace --fb2-flatten leaf
```

## Filter and split templates

Templates use Go `text/template`, slim-sprig functions, and the extra helpers
`oneOf`, `containsValue`, and `rangeName`.

### Evaluation order

1. Select content.
2. Select metadata and series; render final INPX fields.
3. Evaluate `--where` for **each candidate row**.
4. Evaluate `--split-by` once per accepted book, using its **first accepted row**.
5. Write all accepted rows for that book to the same `.inp` member.

The filter accepts `true`, `1`, `yes`, or `on`, and drops empty output, `false`,
`0`, `no`, or `off`. A series filter can keep one row and drop another for the
same book:

```sh
metabib inpx --input library --output cycle --sequence all --prefer-fb2 merge \
  --where '{{eq .Series "Cycle"}}'
```

Templates see normalized output values, such as `.Lang = "ru"`, not raw `RU`
or `russian`. Language normalization does not infer absent language claims.
Use `{{ne .Lang ""}}` to exclude missing values or
`{{default "unknown" .Lang}}` to give them an explicit split bucket.

### Examples

```sh
metabib inpx --input library --output ru-sf \
  --where '{{and (eq .Lang "ru") (containsValue .Genres "sf_history")}}'
metabib inpx --input library --output by-lang-state \
  --split-by '{{if .Deleted}}deleted-{{default "unknown" .Lang}}{{else}}{{default "unknown" .Lang}}{{end}}'
metabib inpx --input library --output by-libid --split-by '{{rangeName .LibID 10000 "other"}}'
metabib inpx --input library --output chunks --split-by '{{rangeName .AcceptedBook 10000 "other"}}'
```

`rangeName VALUE SIZE FALLBACK` makes zero-padded inclusive ranges from a
positive numeric value. For example, `.LibID = "103995"` and size `10000` produce
`0000100001-0000110000.inp`. Invalid, empty, or non-positive values return the
fallback; invalid bucket sizes fail the run.

Splitting **does not sort rows**. Single-valued fields such as language work
well. Multi-valued fields such as genres need an explicit choice, for example
`{{index .Genres 0}}` when non-empty. A book is never copied into multiple split
members.

### Available fields

| Field | Meaning |
| --- | --- |
| `.InputRecord` | 1-based streamed dataset record ordinal. |
| `.BookRow` | 1-based candidate sequence-row ordinal within the book, before filtering. |
| `.AcceptedBook` | 1-based accepted-book ordinal; `0` during `--where`. |
| `.AcceptedRow` | 1-based accepted-row ordinal; `0` during `--where`. |
| `.Author`, `.Genre`, `.Title` | Rendered INPX fields; `.Genre` is colon-separated. |
| `.Series`, `.SerNo` | Current row's rendered series fields. |
| `.File`, `.Ext`, `.Folder` | Rendered file locator fields. |
| `.Size`, `.LibID`, `.Date`, `.Lang` | Rendered size, ID, date, canonical language. |
| `.LibRate`, `.Keywords`, `.Year` | Rendered rating, keywords, and year. |
| `.Deleted` | Boolean deletion state from INPX `DEL`. |
| `.InsNo` | Integer archive entry position written to `INSNO`. |
| `.Authors` | Selected structured authors, before rendered-name sanitization. |
| `.Genres` | Selected genre-code string slice. |
| `.Sequences` | Selected series; each has `.Name`, `.Number`, `.Source`. |
| `.HasDatabase`, `.HasFB2` | Whether those observations exist. |
| `.ArchiveID`, `.ArchiveName` | Dataset source ID and archive filename. |

Each `.Authors` item has `.FirstName`, `.MiddleName`, `.LastName`, `.NickName`,
and `.ID`; the ID is the `flibusta.person` identity when present.

Runs log loaded, written, filtered, skipped, and per-split counts so unintended
lossy filtering is visible without debug logging.

## Additional FLibrary artifacts

`flib-inpx --additional` and `inpx --additional` can write:

| Output | When available |
| --- | --- |
| `prefix-annotations.zip` | Annotations for accepted archive records, including USR content. |
| `prefix-compilations.zip` | FB2 fingerprints exist and compilations are detected; contains compact `compilations.json`. |

The prefix includes the date, for example
`flibusta_20260603-annotations.zip`. Database-only `flib-inpx` input has no
archive-derived additional data, so `--additional` is ignored with a warning.

### Annotation source

| Condition | Selection |
| --- | --- |
| DB annotation exists, mode `ignore` or `complement` | DB annotation. |
| DB annotation exists, mode `merge` or `replace` | FB2/FBD if present, otherwise DB. |
| No DB annotation | FB2 first, then FBD sidecar. |

Partial fingerprint coverage is accepted with a warning. Without fingerprints,
the compilation artifact is skipped.

Additional output is intentionally limited to data in the merged book dataset.
FLibrary's external user reviews and author annotations would require
Flibusta-only `libreviews`/`lib.a.*` inputs and reader-specific identity/layout
coupling; they are not generated.

## Author disambiguation

These settings control stable suffixes for colliding database author names:

```yaml
inpx:
  disambiguate_authors: true
  author_disambiguation_field: last
```

The database cache detects different contributor IDs with the same cleansed,
non-truncated `LastName,FirstName,MiddleName`. The selected `--content` chooses
FB2-only, USR-only, or all-book collision groups.

A unique nickname is the preferred suffix; otherwise the Flibusta person ID is
used. `author_disambiguation_field` chooses where it appears:

| Setting | Example |
| --- | --- |
| `last` (default) | `Васильев [археолог],Сергей,Александрович:` |
| `first` | `Васильев,Сергей [археолог],Александрович:` |
| `middle` | `Васильев,Сергей,Александрович [археолог]:` |

An ID fallback looks like `Абрамов [#17376],Александр,Иванович:`.
FB2-only authors are unchanged because they have no reliable DB contributor IDs.
USR-only name collisions do not affect `--content fb2`.

To investigate suffixes:

```sh
metabib inspect --input library --verbose
```

If disambiguation metadata changes, regenerate the affected database manifest,
merge again, then export INPX. `cache --rebuild` regenerates stale or incompatible
manifests, but does not force a fresh compatible one to rebuild.

### Name sanitization

Before rendering `AUTHOR`, generators trim leading/trailing spaces, commas, and
colons, collapse whitespace, and replace internal ASCII commas/colons with
fullwidth `，`/`：`. Invalid replacement characters make that component empty.
This prevents names from being mistaken for INPX delimiters.
Template `.Author` is rendered; structured `.Authors` keeps raw selected claims.

## Language normalization

By default, INPX export canonicalizes language values without changing the
merged dataset. Database language wins, except ignored placeholders allow an FB2
fallback. Output uses the base language: `RU` → `ru`, `en-US` → `en`,
`sr-Latn` → `sr`.

```yaml
inpx:
  language:
    canonicalize: true
    aliases:
      sp: es
      gr: el
      un: und
    fallback_locales: [en, ru, bg]
    ignore_patterns:
      - '^\?+$'
    context_rules:
      - from: ba
        to: krc
        when_any_source_language: [krc, balkar]
      - from: xa
        to: xal
        when_any_source_language: [xal]
```

- Aliases ignore case after whitespace collapse. Full-value aliases are checked
  before comma splitting, so phrase aliases may contain commas.
- Without a full-value alias, comma-separated parts are tried left to right.
- `fallback_locales` supplies BCP 47 display locales for language-name matching
  after same-record context does not match.
- `ignore_patterns` skips placeholder values.
- `context_rules` correct a value only when another source-language observation
  in the same record supports it.

Unresolved values are emitted raw with warnings identifying the book, source,
observation, locator, artifact, and language evidence. Global `--verbose` also
logs ignored values and successful changes.

## Output details and compatibility

### Filename fields

`FILE` and `EXT` are lookup keys, not display text. Their physical spelling is
preserved, including non-breaking spaces, percent signs, and tabs.
Only INPX structural characters are escaped:

| Character | Escape |
| --- | --- |
| Field separator `0x04` | `~04` |
| Carriage return | `~0D` |
| Line feed | `~0A` |

This is a metabib convention, **not URL encoding**. Consumers need matching
decode support. Literal `~04`, `~0D`, or `~0A` in names is ambiguous and produces
a warning.

### Collection templates

Shared settings live under `inpx`; historical MHL-only settings such as
`quick_fix` and `limits` are also there.

`inpx.comment_template` renders `collection.info`; `inpx.version_template`
renders `version.info`. Both use Go `text/template` with slim-sprig and the
values `.DatabaseName`, `.DumpDate`, and `.DisplayDate`.
Keep the leading `\ufeff` BOM in the comment template when the reader expects it.
Use `metabib dumpconfig --default` to see the complete defaults.

### Publication and logs

An existing INPX is replaced only after the new archive is fully written, with
an overwrite warning. Logs show input selection, record-loading progress, each
created `.inp` member, and final totals.

`elapsed` is wall-clock time. `*_cumulative_elapsed` sums work across batches or
parallel workers, so it can exceed wall-clock time.
