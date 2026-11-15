# Datasets: merge, inspect, and trace

[User guide](guide.md) · [Cache manifests](cache.md) · [JSON schemas](schemas.md)

A merged dataset is the reusable input for inspection and INPX generation.
It keeps source evidence and provenance, rather than reducing metadata to
reader-specific INPX fields.

## Contents

- [Merge manifests](#merge-manifests)
- [Database matching](#database-matching)
- [Consistency checks](#consistency-checks)
- [Annotations and fingerprints](#annotations-and-fingerprints)
- [Inspect a dataset](#inspect-a-dataset)
- [Trace a record to its sources](#trace-a-record-to-its-sources)

## Merge manifests

```sh
metabib merge --database-dumps /path/to/sql-dumps --archives /path/to/books --output library
```

Select archives only, a database only, or both. `--archives` accepts a file or
directory and can be repeated, for example to add a USR collection.

**Merge reads existing manifests.** It never starts MariaDB or reparses archives.
Missing, invalid, or stale selected manifests cause failure by default.

| Option | Purpose |
| --- | --- |
| `--output PREFIX`, `-o PREFIX` | Required output prefix. |
| `--output-compression MODE` | `zstd` (default), `gz`, `zip`, or `none`. |
| `--check-md5` | Verify source checksums recorded in manifests. |
| `--allow-stale` | Warn and continue with stale manifests. |
| `--allow-missing` | Skip selected sources with no manifest. |

`--allow-missing` is mainly for investigating partial caches. Skipped sources
are absent from both the dataset header and records; if every selected source
is missing, merge still fails. For normal runs, build the missing caches first.

Archive-only merge needs no database manifest. A database manifest is required
only when `--database-dumps` is selected.

### Output and input names

`--output library` writes exactly one artifact, normally `library.jsonl.zst`.
Other containers produce `.jsonl.gz`, `.jsonl.zip`, or plain `.jsonl`.
Zstandard uses the same compression level as manifests. Output is published
atomically; replacing an existing output logs a warning.

Later commands accept either that exact file name or the prefix `library`.
A prefix must resolve to exactly one supported dataset file.

The first JSONL value is a `metabib.dataset/1` header with source declarations,
dump date, archive layout, processing options, and ordering. Subsequent values
are `metabib.dataset_record/1` records.

## Database matching

An archive record's physical identity is its **dataset archive ordinal and ZIP
entry index**, not a book ID inferred from its filename.

With database enrichment enabled, matching follows this order:

1. Try a positive numeric entry stem, such as `42` from `42.fb2`.
2. If an exact filename alias including extension points to a different database
   book, use that alias instead: it describes the physical file more precisely.
   Preserve the numeric identity as evidence and add `catalog_id_conflict`.
3. If no numeric database row exists, try a unique filename alias.

The database observation records the match method. Matching never changes the
physical locator.

| Entry | Result |
| --- | --- |
| `42.fb2` | Tries database book `42` through `numeric_entry_stem`. |
| `Some.Book.fb2` | Can match a unique filename alias. |
| `notes.fb2`, no match | Stays a valid archive-only record; no invented catalog identity. |
| `1968.pdf`, alias points to another book | Alias wins; numeric `1968` remains inferred evidence. |

### Alias normalization and collisions

Aliases are trimmed and case-folded. Exact filenames with extensions are kept.
Bare nonnumeric stems are not indexed when the extension is known, because a
title-like stem may refer to several formats. A bare numeric stem is indexed
only when it equals the database book ID.

When the same normalized alias points to different books:

- an active book (`deleted=0`) wins over a deleted one;
- with equal deletion state, an alias owner with joined-book resolution wins
  over an otherwise unlinked owner;
- otherwise the alias is removed from the index and a debug message reports
  `Ambiguous database filename ignored`.

The selected book still owns the alias; joined metadata is tie-break evidence
and is preserved as a relation.

Conflicts never merge or reorder physical entries. For example, `844654.pdf`
can match database book `844910` through its exact alias and retain a conflict
issue. If `844910.pdf` is also an archive entry, it remains a separate record.
Both files survive INPX export unless a later filter drops one.

## Consistency checks

After matching, merge compares cached evidence and records disagreements.
These checks **do not reject records, change matches, or alter INPX preferences**.

| Check | Issue code | Comparison |
| --- | --- | --- |
| `content_md5` | `content_md5_mismatch` | Calculated entry MD5 vs. database MD5. |
| `document_type` | `document_type_mismatch` | Physical/inferred extension vs. database type. |
| `content_size` | `content_size_mismatch` | Uncompressed entry size vs. database size. |
| `title` | `title_mismatch` | Normalized database and FB2/FBD book titles. |
| `authors` | `authors_mismatch` | Usable author lists with no matching name. |
| `language` | `language_mismatch` | Resolved base languages. |

All six checks run by default, in the order above:

```yaml
processing:
  merge_consistency:
    enabled: true
    checks: [content_md5, document_type, content_size, title, authors, language]
```

Select a subset, use `checks: []`, or set `enabled: false`. Unknown or duplicate
names are rejected. List order does not change the check order.
The dataset header records enabled checks and the comparison-policy version
under `processing.merge_consistency`.

### Interpret the issues

Issues have stage `consistency` and retain original/normalized values,
observation references, and matching evidence.

- Artifact disagreements have `high` confidence; bibliographic ones have `low`.
  This measures confidence in the disagreement, not proof of different works.
- Equal MD5 does not suppress metadata disagreements.
- Missing, invalid, placeholder, and quality-filtered values are inconclusive.
- Existing `match` issues remain separate.

<details>
<summary>Comparison rules and inconclusive evidence</summary>

- MD5 and size checks are skipped for nested containers whose inner-document
  byte scope is unknown. Known inner types and FBD metadata can still be compared.
- MD5 must be 32 hexadecimal digits, excluding all-zero and all-F placeholders.
  Sizes must be positive.
- Types ignore case and leading dots; aliases include `htm`/`html`,
  `djv`/`djvu`, and `jpeg`/`jpg`.
- Titles use Unicode normalization/case folding, whitespace collapse, and
  quote/dash normalization without removing meaningful letters or numbers.
- Authors ignore order and source-specific IDs. Initials or incomplete names
  can make a disjoint-list verdict inconclusive.
- Languages reuse configured INPX aliases, ignore patterns, and display locales,
  even if INPX language selection is disabled. Unknown, multilingual, and fuzzy
  display-name-prefix values are skipped.
- Bibliographic comparisons use book `title_info`, not original-work or
  document-info metadata.

</details>

Run merge again to add checks to an existing dataset. Rebuild caches only if
needed evidence, such as content MD5, was not collected.

```sh
metabib inspect --input library --issues
metabib trace --input library --issues
```

### Merge log counters

Merge writes an `Archive manifest merged` summary per archive and final
`Archive manifests merged` totals.

| Field | Meaning |
| --- | --- |
| `db_id_filename_conflict_issues` | Numeric-ID vs. filename-alias matching conflicts, not consistency failures. |
| `consistency_issue_records` | Records with at least one consistency issue. |
| `consistency_issues` | Total consistency issues; a record may have several. |
| `consistency_issues_by_code` | Counts by issue code, stage `consistency` only. |
| `consistency_checks` | Active checks. |

Per-archive counts are not cumulative. With no active checks, consistency
counters are omitted rather than suggesting a clean result. Other issue stages
can make `inspect` totals larger. Merge does not warn for every individual issue.

## Annotations and fingerprints

### Annotation selection

Merge emits at most one database annotation claim per book:

1. If only one retained database annotation exists, use it.
2. Otherwise, find annotations whose title matches the database book title
   case-insensitively, and choose the largest `nid`.
3. If none matches, use the retained annotation with the largest `nid` and log
   a debug message.

The claim stores the body as `value` and the full selected annotation
(`nid`, `title`, `body`) as `raw` provenance.

### Body fingerprints

When caches contain FB2 body fingerprints, the header reports coverage as
`none`, `partial`, or `complete`. Each FB2 artifact may have an `fp` payload:
base64url without padding, encoding the synthetic root and compilation-relevant
sections. Sections with fewer than 100 normalized words are omitted.

### INPX author metadata

Scoped author-ambiguity metadata is copied from the database manifest into the
dataset header. Changes to disambiguation logic require regenerating that
manifest and merging again; exporting INPX from an old dataset cannot see the
new metadata.

## Inspect a dataset

With no mode flag, `inspect` reads only the header and prints a summary:

```sh
metabib inspect --input library
```

The summary includes schema, record count, sources, processing settings,
fingerprint coverage, and author-ambiguity counts. Add `--verbose` to show the
actual ambiguous author maps for all, FB2-only, and USR-only scopes.

### Modes

| Option | What it returns |
| --- | --- |
| `--archives` | Archive source IDs, ordinals, entry counts, names, and path hints. |
| `--validate` | Full-stream validation plus records read and issue totals by stage/code. |
| `--issues` | Records containing issues, with locators and structured payloads. |
| `--book-id ID` | First record matching that library's book ID in a locator, catalog identity, or source observation. |
| `--archive ID --index N` | Record at zero-based entry index `N` in that source. |
| `--file NAME` | First artifact/occurrence filename match, case-insensitively. |
| `--decode-fp` | Decode `fp` on a returned record into root-first sections with depth, leaf flag, and MD5 key. |
| `--json` | Machine-readable output for any mode. |

For example:

```sh
metabib inspect --input library --archives
metabib inspect --input library --archive archive-0001 --index 42
metabib inspect --input library --file 12345.fb2 --json
```

Use only one mode at a time; `--validate` cannot be combined with lookup,
archive-list, or issue modes. `--json` works with every mode.
Record lookups include the optional raw `fp` payload in text and JSON output.

Catalog identities currently use the historical scheme name `flibusta.book` for
both Flibusta and Librusec datasets. The numeric value is the input library's
book ID, not a cross-library mapping. See [identity names](schemas.md#identity-names)
for the corresponding contributor and series schemes.

A lookup with no match exits with status `4`; other failures use `1`.
Archive source IDs are local to one dataset. Use names, path hints, and checksums
to correlate archives across regenerated datasets.

## Trace a record to its sources

`trace` walks from a dataset record back to raw manifests and source files:

```sh
metabib trace --input library --book-id 12345
metabib trace --input library --file 12345.fb2
metabib trace --input library --archive archive-0001 --index 42
metabib trace --input library --issues
```

Unlike `inspect`, a non-issue selector must match **exactly one** record.
If several match, trace lists candidates and fails instead of guessing.
`--issues` traces every record containing issues.

Each step reports `found`, `missing`, `not_applicable`, or `error`.
Missing upstream files do not fail the command as long as the dataset record
was found.

| Option | Purpose |
| --- | --- |
| `--records-full` | Include the full dataset and raw manifest records. |
| `--json` | Machine-readable output using `metabib.trace/1`. |
| `--deep` | Open a source ZIP and verify entry evidence, sizes, available content MD5, and direct sidecars. |

Deep verification currently supports ZIP only. Other archive types report
`not_applicable` for that step; manifest tracing still works.

### Moved datasets or source files

Paths are inferred from the dataset header and current configuration.
Override them when files have moved:

```sh
metabib trace --input /path/to/library.jsonl.zst --book-id 867527 \
  --archive-dir /path/to/books \
  --archive-manifest-dir /path/to/manifests \
  --database-manifest-dir /path/to/sql-dumps
```

| Override | How it is used |
| --- | --- |
| `--archive-path FILE` | Exact source archive. |
| `--archive-dir DIR` | Rewrite archive path hints by basename. |
| `--archive-manifest FILE` | Exact archive manifest. |
| `--archive-manifest-dir DIR` | Derive and scan archive manifests. |
| `--database-manifest FILE` | Exact database manifest. |
| `--database-manifest-dir DIR` | Derive and scan the database manifest. If `database.manifest.zst` exists there, rewrite dump paths by basename too. |
| `--database-dump-dir DIR` | Explicit dump-path rewrite by basename. |

### Terminal output

Both `inspect` and `trace` use compact, human-oriented output by default.
Colors are enabled only for interactive terminals, and disabled by `NO_COLOR`
or redirection. JSON is never colorized.
