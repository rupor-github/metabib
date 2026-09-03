<table>
  <tr>
    <td width="120" valign="middle">
      <img src="docs/library.svg" width="96" alt="metabib" />
    </td>
    <td valign="middle">
      <h1>Metadata extractor from Flibusta/Librusec SQL dumps and FB2/USR archives.</h1>
    </td>
  </tr>
</table>

## metabib
[![GitHub Release](https://img.shields.io/github/release/rupor-github/metabib.svg)](https://github.com/rupor-github/metabib/releases)

`metabib` extracts metadata from Flibusta/Librusec SQL dumps and book archives
into JSON Lines. It first builds cache manifests for database dumps and/or
archives, then merges those cached artifacts into final JSONL.

`metabib` is intentionally focused on current Flibusta/Librusec metadata
workflows: SQL dump metadata, main archive sets, supplemental `usr` archive sets,
and non-FB2 archive entries that should be carried through the same catalog
dataset. FB2-specific parsing and enrichment are applied when FB2 descriptions
are present. Unsupported areas include:

- dump schemas outside the current supported schemas;
- reader-specific INPX quirks that are not part of the generated formats;
- INPX daily updates.

Instead, `metabib` aims to provide an easily parsable source of truth for catalog
programs. INPX is useful as an interchange artifact, but it is far from optimal
as a primary metadata source because it carries reader-specific field and layout
constraints rather than representing a neutral catalog model.

`metabib` caches information extracted from SQL dumps and book archives into
manifest files so expensive extraction work can be reused later. Database dumps
and archives have separate manifests, which makes it possible to update
database-derived metadata without re-parsing the whole archive set. Cached
manifests and combined output records are JSON data with well-defined schemas,
making the resulting dataset easy to validate, transform, and consume from other
tools.

Schema definitions are maintained in [`docs/`](docs/):

- dataset header: [`metabib-dataset.schema.json`](docs/metabib-dataset.schema.json);
- dataset records: [`metabib-dataset-record.schema.json`](docs/metabib-dataset-record.schema.json);
- source cache records: [`metabib.schema.json`](docs/metabib.schema.json);
- archive cache manifests: [`metabib-archive-manifest.schema.json`](docs/metabib-archive-manifest.schema.json);
- database cache manifests: [`metabib-database-manifest.schema.json`](docs/metabib-database-manifest.schema.json).

Current schema versions:

- merged datasets use a `metabib.dataset/1` header and `metabib.dataset_record/1`
  rows;
- manifest payload records use `metabib.record/1`;
- legacy FB2-only archive manifest headers may use `metabib.archive_manifest/1`,
  which remains accepted so existing manifests do not need costly rebuilds;
- generated archive manifest headers use `metabib.archive_manifest/2`; this adds
  the `scope` field and records `fb2` or `usr` archive scope;
- database manifest headers use `metabib.database_manifest/2`.

## Table of Contents

- [What It Does](#what-it-does)
- [MariaDB Binaries](#mariadb-binaries)
- [Usage](#usage)
  - [Fetch Remote Updates](#fetch-remote-updates)
  - [Roll Up Daily Archives](#roll-up-daily-archives)
  - [Build Cache Manifests](#build-cache-manifests)
  - [Merge Dataset](#merge-dataset)
  - [Inspect Dataset](#inspect-dataset)
  - [INPX Generation](#inpx-generation)
    - [`mhl-inpx`](#mhl-inpx)
    - [`flib-inpx`](#flib-inpx)
    - [`inpx`](#inpx)
  - [Configuration](#configuration)
  - [Flibusta Script](#flibusta-script)

## What It Does

`metabib` is organized around reusable processing passes:

- `fetch` downloads new daily archive updates and SQL dumps from a configured
  remote library profile;
- `rollup` folds daily FB2 and USR update ZIPs into local archive ZIPs using
  size, rolling-duration, or UTC calendar-bucket finalization;
- `cache` imports SQL dumps, queries database metadata, and builds reusable
  manifests for each selected source. Archive cache processing understands both
  FB2 and USR scopes: FB2 archives parse FictionBook descriptions from `.fb2`
  entries, while USR archives keep non-FB2 entries, pair supported `.fbd` sidecar
  metadata, and can inspect nested book containers when enabled;
- `merge` reads existing manifests and combines database-derived and
  archive-derived metadata into one provenance-aware dataset JSONL stream with a
  `metabib.dataset/1` header and `metabib.dataset_record/1` rows;
- `inspect` summarizes and validates merged dataset JSONL or locates individual
  records without producing another artifact;
- `mhl-inpx` consumes the merged dataset JSONL to produce a MyHomeLib-compatible
  INPX without coupling the main extraction pipeline to INPX output constraints;
- `flib-inpx` consumes the same merged dataset JSONL to produce a
  FLibrary-compatible INPX with extended fields and multiple flat series links;
- `inpx` consumes the merged dataset JSONL to produce archive-backed INPX output
  for accepted archive entries with `FOLDER` and `INSNO` locators, supports
  Go-template filters through `--where`, can split accepted rows into multiple
  `.inp` members through `--split-by`, and can write FLibrary-compatible
  additional artifacts when FB2-derived source data is available for the accepted
  book set.

Both current Flibusta and current Librusec SQL dump schemas are supported. The
database cache pass autodetects the dump schema and records it in the database
manifest so incompatible manifests are not reused accidentally.

The same transformation approach can support other derived artifacts later,
including update lineages and differential update schemes.

## MariaDB Binaries

When the cache pass processes SQL dumps in the default managed mode, `metabib`
discovers MariaDB binaries recursively in `./mariadb` first, then in `PATH`,
starts a private local MariaDB server, imports `*.sql` dumps with the discovered
`mariadb` or `mysql` client, and stops the server with `mariadb-admin` or
`mysqladmin` when processing is done. It does not require a system database
service. If the optional admin client is unavailable, managed shutdown falls
back to signaling the private server process.

To use an existing MariaDB service instead of the managed local instance, set
`database.dsn` or `database.managed: false` in the configuration file.

The easiest portable setup for managed mode is to keep a local MariaDB unpacked
next to the `metabib` executable or project checkout.

This approach should allow `metabib` to run on any platform supported by Go that
also has recent MariaDB binaries available, whether those binaries come from the
system, a system package, or a separately compiled distribution for that
platform. Finding suitable MariaDB binaries for a particular platform is the
user's responsibility.

On Windows, download MariaDB from <https://mariadb.org/download/>, select the
ZIP archive package, and unzip it into a `mariadb` directory inside the `metabib`
directory. `metabib` will discover binaries such as `mariadbd.exe`,
`mariadb-install-db.exe`, `mariadb.exe`, and `mariadb-admin.exe` from that tree
automatically.

The same ZIP/tarball approach also works on Linux. On Linux it is often simpler
to install the distribution package instead, for example:

```sh
sudo apt install mariadb-server -y
```

If you only want the binaries available for `metabib` managed mode and do not
want MariaDB running as a system service, disable the service after installing
it, for example:

```sh
sudo systemctl disable mariadb
```

On Synology, install the `MariaDB 10` package and point `metabib` at the packaged
binaries explicitly, for example:

```yaml
version: 1
processing:
  manifests:
    archive_dir: "/volume4/backup/library/manifests"
database:
  server_path: "/volume4/@appstore/MariaDB10/usr/local/mariadb10.11/bin/mariadbd"
  install_db_path: "/volume4/@appstore/MariaDB10/usr/local/mariadb10.11/bin/mariadb-install-db"
  client_path: "/volume4/@appstore/MariaDB10/usr/local/mariadb10.11/bin/mariadb"
  admin_path: "/volume4/@appstore/MariaDB10/usr/local/mariadb10.11/bin/mariadb-admin"
```

## Usage

### Fetch Remote Updates

Download new daily archive ZIPs and current SQL dumps using a configured remote
library profile:

```sh
metabib fetch --library flibusta --to upd_flibusta --tosql flibusta_20260622 --continue
metabib fetch --library flibusta-all --to upd_flibusta --tosql flibusta_20260622 --continue
metabib fetch --library librusec --to upd_librusec --tosql librusec_20260713 --continue
metabib fetch --library librusec-usr --to upd_librusec_usr --tosql librusec_20260713 --continue
metabib fetch --library flibusta --noarchives --tosql flibusta_20260622
```

`fetch` reads profiles from the `fetch` section of the YAML configuration,
tracks FB2 and USR high-water marks independently from existing range-named ZIPs
in `--to`, downloads only newer daily archive updates for the selected profile,
and decompresses downloaded `*.sql.gz` dumps into `--tosql`.
FB2 rollup archives such as `fb2-000001-000100.zip`, USR rollup archives such as
`usr-000001-000100.zip`, active `.merging` archives, and retained daily updates
count toward that family's local high-water mark. When `--tosql` is omitted, the
SQL output directory is generated from the library name and current UTC timestamp.
Use `--nosql` to download archive updates only, or `--noarchives` to download SQL
dumps only. `--to` is required unless `--noarchives` is set. `--nosql` and
`--noarchives` cannot be used together.

FB2 and USR are maintained as separate update lineages. A newer FB2 archive or
`fb2-*.merging` file does not suppress USR downloads, and a newer USR archive or
`usr-*.merging` file does not suppress FB2 downloads. Combined profiles such as
`flibusta-all` and `librusec-all` classify each matched remote update first, then
compare it only with that lineage's high-water mark.

The default configuration includes these fetch profiles:

- `flibusta`: Flibusta FB2 daily archives and SQL dumps.
- `flibusta-usr`: Flibusta non-FB2 daily archives and SQL dumps.
- `flibusta-all`: Flibusta FB2 plus non-FB2 daily archives and SQL dumps.
- `librusec`: Librusec FB2 daily archives and SQL dumps.
- `librusec-usr`: Librusec non-FB2 daily archives and SQL dumps.
- `librusec-all`: Librusec FB2 plus non-FB2 daily archives and SQL dumps.

USR selection uses `regexp2` negative lookahead so any daily update extension
except `fb2` is selected without maintaining an extension allowlist.

Flibusta SQL selection downloads `lib.lib*.sql.gz` dumps and
`lib.b.annotations.sql.gz`. It does not download `lib.b.annotations_pics.sql.gz`
or `lib.a.*` dumps.

Exit code `0` means no new archive updates were downloaded, exit code `1` means
an error occurred, and exit code `2` means one or more new archive updates were
downloaded. SQL-only fetches with `--noarchives` return code `0` on success. Use
code `2` to decide whether archive rollup or index/cache rebuild work is needed.

Available `fetch` arguments:

- `--library NAME`, `-l NAME`: fetch profile name from configuration. Default is
  `flibusta`.
- `--to DIR`, `-o DIR`: destination directory for daily archive ZIPs; required
  unless `--noarchives` is set.
- `--tosql DIR`: destination directory for decompressed SQL dump files.
- `--nosql`: skip SQL dump downloads.
- `--noarchives`: skip daily archive ZIP downloads.
- `--retry N`: download attempts per index or file. Default is `3`.
- `--timeout SECONDS`: per-request timeout. Default is `20`.
- `--chunksize MB`: download chunk size used while streaming files. Default is
  `10`.
- `--continue`: resume partial downloads when the server supports ranges.
- `--sticky`: ignore HTTP redirects and keep using the original host.

### Roll Up Daily Archives

Roll downloaded daily update ZIPs into local FB2 and USR archives:

```sh
metabib rollup --archives flibusta --updates upd_flibusta
```

`rollup` keeps finalized archives and active `.merging` archives in `--archives`,
reads daily update ZIPs from each `--updates` directory, classifies updates with
`rollup.update_patterns`, and appends ZIP entries without recompressing them. If
no `--updates` directory is provided, `rollup` scans `--archives` for update ZIPs
as well. Generated archive names use the ID width of the existing `.merging`
archive or latest finalized archive in the same family; new archive directories
default to 10-digit IDs.
Daily update ZIPs are always preserved; retention and cleanup are separate
operational concerns.

Rolled-up archive names are always local range names such as
`fb2-0000817672-0000818248.zip` and `usr-0000817672-0000818248.zip`, including
when source updates are dated Librusec ZIPs.

Rollup also maintains these lineages independently. One invocation can consume a
mixed update directory and update both active archives, for example producing
`fb2-0000886760-0000887123.merging` and
`usr-0000886760-0000887123.merging` from the same `--updates` directory. Each
lineage has its own latest finalized archive, active `.merging` archive, and
overlap checks. Both lineages always use the same finalization policy.

`rollup.update_patterns` are regexp2 filename patterns. The first two capture
groups must be range begin and end, and `family` selects destination lineage
(`fb2-*` or `usr-*`). If one update file matches multiple patterns, rollup fails
with an ambiguity error so precedence is never hidden in code.

Rollup finalization is configured once under `rollup.finalization` and always
applies to both `fb2` and `usr`. The default `size` policy preserves the original
behavior and finalizes active archives when their compressed size reaches the
per-lineage target in binary mebibytes:

```yaml
rollup:
  finalization:
    policy: size
    size:
      target_mib:
        fb2: 2048
        usr: 4096
```

Period policies ignore size targets. `rolling` finalizes an active `.merging`
archive after a whole-day or whole-week duration from the time that merge first
started accumulating entries:

```yaml
rollup:
  finalization:
    policy: rolling
    rolling:
      duration: 14d
```

Accepted rolling units are `d` and `w`; sub-day durations such as `24h` are
rejected.

`calendar` finalizes when current UTC time leaves the stored bucket:

```yaml
rollup:
  finalization:
    policy: calendar
    calendar:
      bucket: month
```

Supported UTC buckets are `iso-week`, `iso-biweek`, and `month`. ISO weeks start
Monday `00:00:00` UTC. ISO biweeks are weeks `1-2`, `3-4`, and so on; ISO week
`53` is a single-week bucket.

Period policies store active merge timing in `rollup-state.json` inside
`--archives`. If a period policy sees an existing `.merging` archive without
matching state for that lineage, rollup fails instead of guessing when the merge
started. Stored state for all active lineages must use the same configured policy.
The `size` policy does not require this state file. When no `.merging` archive
exists for a lineage, period rollup starts that lineage cleanly and creates state
only after it publishes the first new active merge.

State file format:

```json
{
  "version": 1,
  "lineages": {
    "fb2": {
      "active_merge": "fb2-0000000001-0000000100.merging",
      "first_book": 1,
      "last_book": 100,
      "policy": "calendar",
      "calendar": "month",
      "bucket_start": "2026-08-01T00:00:00Z",
      "bucket_end": "2026-09-01T00:00:00Z"
    },
    "usr": {
      "active_merge": "usr-0000000001-0000000100.merging",
      "first_book": 1,
      "last_book": 100,
      "policy": "calendar",
      "calendar": "month",
      "bucket_start": "2026-08-01T00:00:00Z",
      "bucket_end": "2026-09-01T00:00:00Z"
    }
  }
}
```

Only the fields for the selected policy are present for each active lineage.
`active_merge`, `first_book`, and `last_book` must match the `.merging` filename
for that lineage. When a lineage finalizes, its state entry is removed. When a
new active merge starts, rollup writes a new state entry atomically.

Finalization logs include both `policy` and `reason`; reasons are `size`,
`rolling_deadline`, or `calendar_bucket_end`.

By default, direct compressed copying does not validate entry payload CRCs. Set
`rollup.validate_crc: true` in the configuration to decompress each non-empty
numeric entry for CRC-32 validation before copying it. Validation can significantly
reduce performance, but entries are still copied in their original compressed form
without recompression.

Exit code `0` means no finalized archive was produced, exit code `1` means an
error occurred, and exit code `2` means one or more finalized `fb2-*.zip` or
`usr-*.zip` archives were created. Use code `2` to decide whether cache/index
rebuild work is needed.

Available `rollup` arguments:

- `--archives DIR`, `-a DIR`: required directory for finalized `fb2-*.zip` and
  `usr-*.zip` archives plus active `fb2-*.merging` and `usr-*.merging` archives.
- `--updates DIR`, `-u DIR`: directory containing daily update ZIPs; can be
  repeated. Defaults to `--archives` when omitted.

### Build Cache Manifests

`cache` creates portable manifest files for selected sources. It does not produce
the final merged dataset JSONL.

```sh
metabib cache \
  --database-dumps /path/to/sql-dumps \
  --archives /path/to/flibusta
```

To use an already imported database:

```sh
metabib cache --rebuild --no-import --database-dumps /path/to/sql-dumps
```

By default, managed mode uses a fresh database for every run:

```yaml
database:
  managed: true
  temporary: true
```

With `temporary: true`, metabib initializes a new managed MariaDB datadir under
the OS temp directory and removes it on shutdown. Persistent managed datadirs are
reused between runs when `temporary` is set to `false`.

Use an existing MariaDB service instead of a managed one:

```sh
metabib --config metabib.yaml cache --rebuild --database-dumps /path/to/sql-dumps
```

Build only archive manifests without starting MariaDB:

```sh
metabib cache --archives /path/to/flibusta
```

`cache` builds missing selected manifests by default. Existing manifests are
checked using source modification times; stale or invalid manifests fail unless
`--rebuild` is used. Use `cache --check-md5` to additionally verify MD5 checksums
recorded in existing manifests.

Manifests are portable across directories and machines. Stored absolute paths are
kept as provenance, but manifest matching uses archive or dump file names,
recorded metadata, processing settings, timestamps for freshness, and optional
MD5 checksums when `--check-md5` is enabled.

Archive manifest scope is selected before an archive manifest is validated or
built:

- directories, backup entries such as `.org`, and `.fbd` sidecar entries are
  ignored while classifying scope;
- archive paths that look like USR input force `usr` scope: the archive basename
  contains `.usr-` or `.usr.`, starts with `usr-`, or any parent directory
  component is `usr`, ends with `_usr`, or ends with `-usr`;
- otherwise, an archive with at least one `.fb2` book entry is `fb2` scope;
- otherwise, an archive with no `.fb2` book entries is `usr` scope;
- if an archive forced to `usr` scope also contains `.fb2` book entries, those
  FB2 entries are ignored and a warning is logged;
- if an archive selected as `fb2` scope also contains non-FB2 book entries, those
  non-FB2 entries are ignored and a warning is logged.

New archive manifests are written as `metabib.archive_manifest/2` with `scope`
set to `fb2` or `usr`. Existing `metabib.archive_manifest/1` files are treated as
legacy FB2 manifests and remain reusable when all other freshness checks pass.
When an archive manifest is rebuilt, the final `Archive manifest created` log
entry includes post-processing ignore counts: `usr_fb2_entries_ignored` for FB2
entries ignored in USR scope and `fb2_non_fb2_entries_ignored` for non-FB2 book
entries ignored in FB2 scope. The USR count includes both filename-detected
entries such as `.fb2.zip` and nested-inspection detections inside opaque
containers. Default logs have one final aggregate count per rebuilt archive.

Nested multi-volume archives in USR scope are not extracted across volumes. When
`cache` detects a complete nested multi-volume set, it emits one opaque record for
the root volume and skips continuation parts. The root record carries a
`nested_archive_multivolume` issue with `volume_format`, `volume_count`,
`volume_entries`, and `volume_indexes` details. Incomplete starts and orphan
continuations remain ordinary bad nested archive issues and are logged as
warnings. Supported detection patterns include RAR sets such as `book.rar` plus
`book1.rar`, `book.part1.rar` plus `book.part2.rar`, and
`book_partiya_1_.rar` plus `book_partiya_2_.rar`; 7z sets use the standard
`book.7z.001`, `book.7z.002`, ... scheme.

Set `processing.fb2_body_fingerprints: true` to calculate compact FB2 body and
section fingerprints while archive manifests are built. This requires
`processing.parse_fb2: true`. Archive manifests built with different fingerprint
settings, model, or section encoding are rejected and must be rebuilt.

By default, `cache` requires all SQL dump files to report the same dump date
before import. Use `cache --allow-dump-date-mismatch` to accept mixed dump dates;
per-file dump dates are still recorded, while the top-level manifest `dump_date`
is omitted.

For current Librusec dumps, only the tables required for FB2 metadata are
imported. Unsupported or unrelated dump files in the SQL directory are ignored by
the importer.

Database manifests also carry INPX-oriented author ambiguity metadata. Since the
database cache pass now covers both FB2 and non-FB2 catalog rows, this metadata is
stored in three scopes: all database books, FB2 books only, and USR/non-FB2 books
only. INPX generators select the scope that matches their `--content` mode so PDF
or other USR-only author collisions do not change FB2 INPX author names.

### Merge Dataset

`merge` consumes existing cache manifests and writes one merged dataset JSONL
artifact for later inspection or INPX generation:

```sh
metabib merge \
  --database-dumps /path/to/sql-dumps \
  --archives /path/to/flibusta \
  --output metabib
```

Merge from archives only, database only, or both:

```sh
metabib merge --archives /path/to/flibusta --output archive-only
metabib merge --database-dumps /path/to/sql-dumps --output database-only
metabib merge --database-dumps /path/to/sql-dumps --archives /path/to/flibusta --output combined
```

`merge` never starts MariaDB and never reads archives directly. It fails when a
selected manifest is missing, invalid, or stale. Use `--check-md5` for full
source checksum verification, `--allow-stale` to warn and continue with stale
manifests, or `--allow-missing` to skip selected sources whose manifests do not
exist yet.

```sh
metabib merge \
  --allow-missing \
  --database-dumps /path/to/sql-dumps \
  --archives /path/to/flibusta \
  --archives /path/to/flibusta_usr \
  --output combined
```

`--allow-missing` omits missing sources from the dataset header and merged output.
It is mostly useful for debugging partial cache state; normal production runs
should build missing manifests with `cache` first. If every selected source
manifest is missing, merge still fails.

Archive-only merge does not require a database manifest. A database manifest is
required only when `--database-dumps` is selected, whether that is for
database-only output or for enriching archive records with database metadata.

Merged JSONL output is zstd-compressed by default, using the same compression
level as manifest files. Use `--output-compression zstd`, `gz`, `zip`, or `none`
to select a different output container. The `--output` value is an output prefix,
not a final file name: `metabib merge --output all` writes exactly one artifact,
such as `all.jsonl.zst`. Existing output files are replaced; when that happens,
`metabib` logs an overwrite warning.

The first JSONL value is a dataset header (`metabib.dataset/1`) with the database
dump date, archive entry layout, processing options, and declared ordering. Every
following value is a dataset record (`metabib.dataset_record/1`). INPX generation
requires this dataset shape and rejects `metabib.record/1` input.

When a database manifest contains scoped INPX author ambiguity metadata, `merge`
copies it into the dataset header. Regenerate the database manifest and run
`merge` again after changes to author disambiguation logic; regenerating INPX from
an old merged JSONL cannot see new scoped metadata.

When archive manifests contain FB2 body fingerprints, merge records dataset-level
fingerprint coverage as `none`, `partial`, or `complete` and copies compact
per-book section fingerprints onto the FB2 artifact as `fp`. The value is a
base64url-no-padding encoded binary payload containing the synthetic root plus
compilation-relevant sections; sections with normalized word count below 100 are
omitted to keep manifests small.

Archive records are anchored by dataset archive ordinal and ZIP entry index.
Physical record order is never inferred from entry filenames or database book IDs.
When database enrichment is enabled, merge first treats a positive numeric entry
stem as matching evidence. If an exact filename alias with extension points at a
different database book, that alias wins because it describes the physical file
more precisely; merge keeps the numeric stem as an inferred archive catalog
identity and records a `catalog_id_conflict` issue. Otherwise merge uses the
numeric row, then tries a unique database filename alias when no numeric database
row exists. The selected database observation records the match method, and
conflicting evidence never changes the physical record locator.

Examples:

- `42.fb2` first tries database book `42` through `numeric_entry_stem`; if present,
  database and FB2 claims share one archive-entry record while the archive locator
  remains the ZIP entry position.
- `Some.Book.fb2` can match a unique database filename alias; the database book ID
  becomes a catalog identity claim, not the physical record position.
- `notes.fb2` with no database match remains a valid archive-only record and has
  no invented catalog identity.
- `1968.pdf` can infer numeric archive catalog identity `1968`, but an exact
  `1968.pdf` database alias for another book wins the database match.

Database filename aliases are normalized with surrounding whitespace removed and
case folded before they are used for matching. Exact filenames with extensions are
kept as aliases. Bare nonnumeric stems are not indexed when an extension is known,
because title-like stems such as `Megan_Lindholm_The_Wizard_of_the_Pigeons` can
refer to several formats. Bare numeric stems are indexed only when the stem equals
the database book ID; `1968.pdf` remains an exact alias, but bare `1968` is not an
alias for some other book whose title or filename happens to be `1968`.

If several normalized aliases still point at different database books, merge tries
to resolve the collision with database lifecycle metadata before declaring the
alias ambiguous. The index still stores the database book that owns the alias;
joined metadata is only tie-break evidence and is preserved as a relation.

- an active (`deleted=0`) book wins over a deleted one;
- if both candidates have the same deleted state, an alias owner with joined-book
  resolution wins over an otherwise unlinked alias owner;
- otherwise the alias is removed from the filename index and a debug log reports
  `Ambiguous database filename ignored`.

Ambiguous or conflicting filename evidence never creates, removes, or reorders
archive entries. If archive entry `844654.pdf` numerically matches database book
`844654`, but an exact filename alias points to book `844910`, merge keeps one
physical `844654.pdf` archive-entry record, matches it to `844910`, and records a
`catalog_id_conflict` issue preserving the numeric stem evidence. If `844910.pdf`
is also present as another archive entry, it is emitted separately as its own
record. INPX output therefore preserves both physical files unless later filters
explicitly drop one.

### Inspect Dataset

Use `inspect` for quick checks and debugging of merged dataset JSONL artifacts:

```sh
metabib inspect --input combined
metabib inspect --input combined --archives
metabib inspect --input combined --validate
metabib inspect --input combined --issues
metabib inspect --input combined --book-id 12345
metabib inspect --input combined --archive archive-0001 --index 42
metabib inspect --input combined --file 12345.fb2 --json
```

`--input` accepts the same prefix or exact dataset path as INPX commands. For
example, `--input combined` discovers exactly one of `combined.jsonl`,
`combined.jsonl.zst`, `combined.jsonl.gz`, or `combined.jsonl.zip`.

With no mode flag, `inspect` reads the dataset header and prints its schema,
record count, source totals, processing settings, and other summary metadata. It
does not scan the record stream in this mode.

Inspect summary output includes FB2 body fingerprint coverage when present. Record
lookup output includes the optional artifact `fp` payload in both text and JSON
modes.

Inspect summary output also includes INPX author ambiguity counts. Use `--verbose`
to print the actual ambiguous DB author maps for all, FB2-only, and USR-only
scopes. This is the quickest way to explain why an INPX author got a suffix such
as `[#17376]` or `[писатель]`.

Available modes and options:

- `--archives`: list archive source IDs, ordinals, entry counts, names, and path
  hints. IDs such as `archive-0001` can then be used with `--archive`.
- `--validate`: stream the full dataset and validate ordering, schemas,
  provenance references, source declarations, and archive indexes without
  writing any derived artifact. Successful output includes the number of records
  read plus issue totals by stage and code.
- `--issues`: stream the full dataset and list records with `issues`, including
  record number, locator, artifact names, and structured issue payloads. Use
  `--json` for machine-readable issue records.
- `--book-id ID`: return the first record matching a primary locator, Flibusta
  catalog identity, or database observation with that book ID.
- `--archive ID --index INDEX`: return the record at the zero-based entry index
  in the specified dataset archive source.
- `--file NAME`: return the first record whose artifact name or archive occurrence
  entry matches `NAME`; matching is case-insensitive.
- `--decode-fp`: when returning a record, also decode compact artifact `fp`
  payloads into root-first section rows with depth, leaf flag, and MD5 hex key.
- `--json`: emit the selected summary, archive list, validation result, or record
  as machine-readable JSON.

Only one of `--archives`, `--issues`, `--book-id`, `--archive`/`--index`, and
`--file` may be used at a time. `--validate` cannot be combined with any of those
modes, while `--json` can be used with every mode. A lookup that finds no matching
record exits with status `4`; other failures use status `1`.

Archive source IDs are local to one merged dataset. Use archive names, path hints,
and checksums for long-term correlation across regenerated datasets.

### INPX Generation

All INPX generators treat filename fields as lookup keys. `FILE` and `EXT`
preserve physical archive-entry spelling more strictly than display fields:
non-breaking spaces, literal percent signs, tabs, and other non-structural bytes
are left unchanged. Only characters that would break the INPX row format are
escaped with a visible tilde sequence: field separator `0x04` becomes `~04`,
carriage return becomes `~0D`, and line feed becomes `~0A`. This is a narrow
metabib convention rather than full URL percent-encoding; INPX consumers need
matching decode support to resolve such archive entries during import or
extraction. Literal `~04`, `~0D`, and `~0A` in archive names are ambiguous escape
sequences; generators warn when such names are encountered.

#### `mhl-inpx`

Build a MyHomeLib-compatible "historical" INPX from merged dataset JSONL:

```sh
metabib mhl-inpx --input all --output flibusta
```

`mhl-inpx` consumes the merged dataset JSONL; it does not read SQL dumps, start
MariaDB, or parse archives directly. Database metadata, FB2 metadata, and
sidecar-derived metadata are read from normalized claims when present.

When the merge input is database-only and has no archives, `mhl-inpx` writes the
records into `online.inp`. When archive metadata is present, `online.inp` is not
created and archive-less records are ignored.

Available `mhl-inpx` arguments:

- `--input PREFIX`, `-i PREFIX`: required input prefix or exact dataset path.
  `metabib mhl-inpx --input all` discovers exactly one of `all.jsonl`,
  `all.jsonl.zst`, `all.jsonl.gz`, or `all.jsonl.zip`.
- `--output PREFIX`, `-o PREFIX`: required output prefix. The dump date from the
  dataset header is appended automatically, so `--output flibusta` writes a file
  named like `flibusta_20260603.inpx`.
- `--content MODE`: content selection. Supported values are `fb2`, `usr`, and
  `all`. Default is `fb2`. Archive records are classified by their logical
  artifact name first; physical archive member names stay in occurrence entries.
  For example, an opaque `.zip` entry inspected as PDF has artifact name `.pdf`
  and occurrence entry `.zip`, so it is not selected as FB2 even when matched
  database metadata says `file_type=fb2`. If nested archive inspection is
  disabled, opaque containers keep their container extension, such as `.zip`, and
  still do not fall back to database `file_type` for archive content selection.
  Database `file_type` remains the fallback for database-only records.
- `--format MODE`: INPX record layout. Supported values are `2x` and `ruks`.
  Default is `2x`. `ruks` appends MD5 and replacement fields when available.
- `--sequence MODE`: database sequence selection. Supported values are `author`,
  `publisher`, and `ignore`. Default is `author`.
- `--prefer-fb2 MODE`: how FB2 metadata is used relative to database metadata for
  authors and sequences. Supported values are `ignore`, `merge`, `complement`,
  and `replace`. Default is `complement`: database authors and sequence data are
  preferred when present, and FB2 metadata fills missing values. Use `replace`
  when FB2 author order should win.

#### `flib-inpx`

Build a FLibrary-compatible INPX from the same merged dataset JSONL:

```sh
metabib flib-inpx --input all --output flibusta
```

`flib-inpx` consumes only merged dataset JSONL. It does not read SQL dumps or
archives directly, emits no dummy records, and always writes `structure.info` with
FLibrary extensions such as `FOLDER`, `YEAR`, and `SOURCELIB`.

Database-only FLibrary INPX generation follows the same `online.inp` rule as
`mhl-inpx`: it is created only when the dataset header contains no archives.

When a book has multiple selected sequences, `flib-inpx` writes repeated `.inp`
rows for the same `FOLDER + FILE + EXT`, one row per flat `SERIES`/`SERNO` link.
FLibrary imports those rows as multiple series relations for one physical book.

Available `flib-inpx` arguments:

- `--input PREFIX`, `-i PREFIX`: required input prefix, discovered the same way
  as `mhl-inpx`.
- `--output PREFIX`, `-o PREFIX`: required output prefix. The dump date is
  appended automatically, so `--output flibusta` writes a file named like
  `flibusta_20260603.inpx`.
- `--content MODE`: content selection. Supported values are `fb2`, `usr`, and
  `all`. Default is `fb2`. Archive records are classified by their logical
  artifact name first; physical archive member names stay in occurrence entries.
  For example, an opaque `.zip` entry inspected as PDF has artifact name `.pdf`
  and occurrence entry `.zip`, so it is not selected as FB2 even when matched
  database metadata says `file_type=fb2`. If nested archive inspection is
  disabled, opaque containers keep their container extension, such as `.zip`, and
  still do not fall back to database `file_type` for archive content selection.
  Database `file_type` remains the fallback for database-only records.
- `--prefer-fb2 MODE`: sequence source preference. Supported values are
  `ignore`, `merge`, `complement`, and `replace`. Default is `complement`.
- `--sequence MODE`: selected sequence class. Supported values are `author`,
  `publisher`, `all`, and `ignore`. Default is `author`.
- `--fb2-flatten MODE`: FB2 nested sequence flattening. Supported values are
  `all`, `leaf`, `path`, and `path-leaf`. Default is `all`.
- `--source-lib VALUE`: `SOURCELIB` field value. Default is the dataset header
  library name.
- `--additional`: also write supported FLibrary additional artifacts next to the
  INPX output. Database-only inputs have no archive-derived additional source
  data, so this flag is ignored with a warning for those datasets.

With `--additional`, `flib-inpx` writes `prefix-annotations.zip` from FB2
annotations and FBD sidecar annotations for accepted archive records, including
USR records selected by `--content usr` or `--content all`. FB2 annotations are
preferred when both FB2 and FBD claims are present. When the input dataset has FB2
body fingerprints and compilations are detected, it also writes
`prefix-compilations.zip` containing compact `compilations.json`. Partial
fingerprint coverage is accepted with a warning; datasets without fingerprints
skip the compilations artifact.

FLibrary-specific settings that are not command-line arguments live under
`inpx.flibrary`:

```yaml
inpx:
  flibrary:
    sequence_dedup: case-insensitive
    fb2_path_separator: " / "
```

`sequence_dedup` supports `case-insensitive` and `case-sensitive`.
`fb2_path_separator` is used by `--fb2-flatten path` and `path-leaf`.

#### `inpx`

Build an archive-backed filtered/split INPX from the same merged dataset JSONL:

```sh
metabib inpx --input all --output ru --where '{{eq .Lang "ru"}}'
```

`inpx` is archive-only. It rejects database-only datasets because its output uses
explicit `FOLDER` and `INSNO` fields to point at source archive entries. When
archive metadata is present, archive-less records are skipped. Filtered records
are omitted entirely, and no dummy records are emitted.

Content selection happens before template filtering. The `inpx` default is
`--content all`, while `--content fb2` keeps only FB2 records and `--content usr`
keeps non-FB2 records. Archive records are classified by logical artifact name
before database `file_type`; physical archive member names stay in occurrence
entries. If nested archive inspection is disabled, opaque containers keep their
container extension for content selection. Database `file_type` is still used for
database-only records.
Filtering then happens after INPX normalization. Templates see final output
fields, including canonicalized language values such as `ru`, not raw source
values such as `RU` or `russian`. Canonicalization only normalizes existing
language values; it does not infer language when both database and FB2 language
claims are absent. Use `{{ne .Lang ""}}` to exclude missing-language records, or
`{{default "unknown" .Lang}}` to route them into an explicit split bucket.

Available `inpx` arguments:

- `--input PREFIX`, `-i PREFIX`: required input prefix, discovered the same way
  as `mhl-inpx`.
- `--output PREFIX`, `-o PREFIX`: required output prefix. The dump date is
  appended automatically, so `--output flibusta` writes a file named like
  `flibusta_20260603.inpx`.
- `--content MODE`: content selection. Supported values are `fb2`, `usr`, and
  `all`. Default is `all`.
- `--where TEMPLATE`: keep rows when the Go template renders `true`, `1`, `yes`,
  or `on`; drop rows when it renders empty output, `false`, `0`, `no`, or `off`.
- `--where-file FILE`: load the filter template from a file. Mutually exclusive
  with `--where`.
- `--split-by TEMPLATE`: write accepted books to `<key>.inp` entries using a Go
  template. Without this flag, all accepted rows go to `books.inp`.
- `--split-by-file FILE`: load the split template from a file. Mutually exclusive
  with `--split-by`.
- `--prefer-fb2 MODE`, `--sequence MODE`, and `--fb2-flatten MODE`: same
  sequence-source and FB2 flattening semantics as `flib-inpx`.
- `--additional`: write FLibrary-compatible additional artifacts for accepted
  books only. Annotation artifacts use FB2 annotations first, then FBD sidecar
  annotations for USR records.

Filter and split templates use Go `text/template` with slim-sprig functions plus
`oneOf`, `containsValue`, and `rangeName` helpers:

```sh
metabib inpx --input all --output by-lang --split-by '{{.Lang}}'
metabib inpx --input all --output ru-sf --where '{{and (eq .Lang "ru") (containsValue .Genres "sf_history")}}'
metabib inpx --input all --output by-lang-state --split-by '{{if .Deleted}}deleted-{{default "unknown" .Lang}}{{else}}{{default "unknown" .Lang}}{{end}}'
metabib inpx --input all --output by-libid --split-by '{{rangeName .LibID 10000 "other"}}'
metabib inpx --input all --output chunks --split-by '{{rangeName .AcceptedBook 10000 "other"}}'
```

Available filter and split template fields:

- `.InputRecord`: 1-based ordinal of the streamed dataset record. Available to
  `--where` and `--split-by`.
- `.AcceptedBook`: 1-based ordinal of the accepted book. It is set for
  `--split-by` and is `0` while `--where` is evaluated.
- `.AcceptedRow`: 1-based ordinal of the accepted output row. It is set for
  `--split-by` and is `0` while `--where` is evaluated.
- `.BookRow`: 1-based ordinal of the current sequence row within the book.
- `.Author`: rendered INPX `AUTHOR` field.
- `.Genre`: rendered INPX `GENRE` field, with colon-separated genre codes.
- `.Title`: rendered INPX `TITLE` field.
- `.Series`: rendered INPX `SERIES` field for the current row.
- `.SerNo`: rendered INPX `SERNO` field for the current row.
- `.File`: rendered INPX `FILE` field.
- `.Size`: rendered INPX `SIZE` field.
- `.LibID`: rendered INPX `LIBID` field.
- `.Deleted`: boolean deletion state derived from the INPX `DEL` source value.
- `.Ext`: rendered INPX `EXT` field.
- `.Date`: rendered INPX `DATE` field.
- `.InsNo`: integer source archive entry position written to INPX `INSNO`.
- `.Folder`: rendered INPX `FOLDER` field.
- `.Lang`: rendered canonical INPX `LANG` field.
- `.LibRate`: rendered INPX `LIBRATE` field.
- `.Keywords`: rendered INPX `KEYWORDS` field.
- `.Year`: rendered INPX `YEAR` field.
- `.Authors`: selected structured authors for the book.
- `.Genres`: selected genre codes as a string slice.
- `.Sequences`: selected sequences for the book; each item has `.Name`,
  `.Number`, and `.Source`.
- `.HasDatabase`: true when the record has database observation.
- `.HasFB2`: true when the record has FB2 observation.
- `.ArchiveID`: dataset archive source ID.
- `.ArchiveName`: source archive file name.

Each `.Authors` item has `.FirstName`, `.MiddleName`, `.LastName`, `.NickName`,
and `.ID`. `.ID` is the `flibusta.person` identity when present.

`rangeName VALUE SIZE FALLBACK` returns zero-padded inclusive range names.
Pass any positive numeric value, such as `.AcceptedBook` or `.LibID`. Invalid,
empty, or non-positive values return `FALLBACK`; invalid bucket sizes fail the
run.

Examples:

- `{{rangeName .AcceptedBook 10000 "other"}}`: split by accepted-book ordinal.
- `{{rangeName .LibID 10000 "other"}}`: split by numeric `LIBID` range.
- `.LibID = "103995"` with size `10000` becomes
  `0000100001-0000110000.inp`.

`--split-by` does not sort rows. Splitting by `.Lang` is safe because language is
single-valued. Splitting by genres or other multi-valued fields is lossy unless
the template explicitly chooses one value, such as `{{index .Genres 0}}`. One
book is never split across multiple `.inp` entries; if it writes multiple
sequence rows, all accepted rows for that book go to the split key from the first
accepted row.

Because templates are flexible, every run logs loaded, written, filtered,
skipped, and per-split counts so accidental lossy filters are visible without
debug logging.

Shared INPX settings live under the `inpx` section of the YAML configuration.
Some fields are used by every INPX generator, while MHL-specific options such as
`quick_fix` and `limits` also live there for historical compatibility.

```yaml
inpx:
  disambiguate_authors: true
  author_disambiguation_field: last
  comment_template: "\ufeff{{ .DatabaseName }} FB2 - {{ .DisplayDate }}\r\n{{ .DatabaseName }}_{{ .DumpDate }}\r\n65536\r\nЛокальные архивы библиотеки {{ .DatabaseName }} (FB2) {{ .DisplayDate }}"
  version_template: "{{ .DumpDate }}\r\n"
  language:
    canonicalize: true
    aliases:
      sp: es
      gr: el
      un: und
    fallback_locales:
      - en
      - ru
      - bg
    ignore_patterns:
      - '^\?+$'
    context_rules:
      - from: ba
        to: krc
        when_any_source_language:
          - krc
          - balkar
      - from: xa
        to: xal
        when_any_source_language:
          - xal
```

`comment_template` and `version_template` are Go `text/template` values rendered
when `collection.info` and `version.info` are written. Available values are
`.DatabaseName`, `.DumpDate`, and `.DisplayDate`; slim-sprig template functions
are available. Keep the leading `\ufeff` BOM when the target reader expects a BOM
at the start of `collection.info`.

INPX generators apply database author disambiguation when
`inpx.disambiguate_authors` is enabled. Database cache manifest creation records
DB authors whose cleansed, non-truncated `LastName,FirstName,MiddleName` value
collides with a different database contributor ID. The manifest stores collision
groups for all database books, FB2 books only, and USR/non-FB2 books only. INPX
generation selects the matching group set by `--content`: `fb2` uses only FB2
collisions, `usr` uses only USR collisions, and `all` uses all collisions.

When a selected DB author belongs to an ambiguous group, the exported INPX
author name receives a stable suffix. By default,
`inpx.author_disambiguation_field: last` preserves existing behavior and appends
the suffix to the last-name field. A unique database nickname becomes the
preferred suffix, for example `Новиков [писатель],Александр,Васильевич:`. If no
unique nickname is available, the suffix falls back to the Flibusta person ID,
for example `Абрамов [#17376],Александр,Иванович:`. Only DB authors with catalog
person identities are disambiguated; FB2-only authors are not changed because they
do not have reliable database contributor IDs.

Set `inpx.author_disambiguation_field` to choose which INPX author component gets
the suffix:

- `last`: `Васильев [археолог],Сергей,Александрович:`;
- `first`: `Васильев,Сергей [археолог],Александрович:`;
- `middle`: `Васильев,Сергей,Александрович [археолог]:`.

Scoped disambiguation prevents non-FB2 catalog rows from changing default FB2
INPX output. For example, if `Коллектив авторов` is ambiguous only because a PDF
row introduces another DB contributor ID, `flib-inpx --content fb2` keeps FB2 rows
as `Коллектив авторов,,:`. `flib-inpx --content all` can still suffix that author
because the all-content INPX needs unique names across both FB2 and USR records.

To inspect author ambiguity metadata in a merged dataset, run:

```sh
metabib inspect --input all --verbose
```

If author disambiguation metadata changes, rebuild the database manifest before
regenerating merged JSONL and INPX output:

```sh
metabib cache --database-dumps /path/to/sql-dumps --rebuild
metabib merge --database-dumps /path/to/sql-dumps --archives /path/to/flibusta --output all
metabib flib-inpx --input all --output flibusta
```

All INPX generators sanitize author name components before joining them into the
rendered INPX `AUTHOR` field. Internal ASCII commas and colons are replaced with
fullwidth characters (`，` and `：`) so they cannot be confused with INPX author
delimiters; leading and trailing spaces, commas, and colons are trimmed,
whitespace is collapsed, and invalid replacement characters make that component
empty. Generic `inpx` template fields such as `.Author` use this rendered value;
structured `.Authors[]` values exposed to templates remain the raw selected
author claims.

INPX generators canonicalize language values at generation time by default.
Raw merged dataset JSONL remains unchanged. Database language has priority over
FB2 language, except ignored placeholder values are treated as absent so FB2 can
be used as a fallback. Resolved output is the base language subtag, so values such
as `RU`, `en-US`, and `sr-Latn` become `ru`, `en`, and `sr`. Explicit aliases
handle known noisy values such as `sp -> es`, `gr -> el`, and `un -> und`.

`aliases` are matched case-insensitively after whitespace collapse. Full-value
aliases are checked before comma splitting, so phrase aliases containing commas
are supported. If no full-value alias matches, the raw language value is split on
commas and parts are tried from left to right until one resolves. `fallback_locales`
are BCP 47 display locales used for localized language-name matching after
same-record context does not match. `ignore_patterns` are regular expressions for
values that should be skipped entirely. `context_rules` apply only when another
source-language observation on the same record supports the correction.

Unresolved canonicalization attempts are emitted raw and logged as warnings with
book ID, source field, observation, original value, candidate value, locator,
artifact, source languages, and context language. With global `--verbose`,
ignored values and successful canonicalizations that change the output are logged
with the same identifying details.

Existing INPX output is replaced only after the new archive is fully written. If
an existing file is overwritten, `metabib` logs a warning. During generation,
`metabib` logs the selected dataset input, record loading progress, one live
message per created `.inp` member, and final aggregate INPX statistics.

Manifest cache files are zstd-compressed JSONL payloads named `.manifest.zst`,
for example `lib.manifest.zst` or `database.manifest.zst`. When archive
manifests are stored in a central `processing.manifests.archive_dir`, the first
archive with a basename keeps the usual manifest name. Later archives with the
same basename get a source-qualified manifest name and a warning is logged.

Use global `--verbose` to enable detailed progress reporting.

### Configuration

Dump the default configuration to a file before customizing paths, logging, fetch
profiles, processing options, or INPX templates:

```sh
metabib dumpconfig --default metabib.yaml
```

`dumpconfig --default` writes the embedded YAML template after expanding runtime
defaults such as the executable directory, OS-specific database socket or TCP
settings, free local ports on Windows, and CPU-based worker counts. The output is
not based on values from a loaded `--config` file.

Dump the effective configuration after applying defaults and a config file:

```sh
metabib --config metabib.yaml dumpconfig effective.yaml
```

Omit the destination to write YAML to stdout:

```sh
metabib dumpconfig --default
metabib --config metabib.yaml dumpconfig
```

Configuration files are validated strictly: unknown YAML fields are rejected, and
omitted fields keep their defaults from the embedded template. Pass the file with
the global `--config FILE` option, before the subcommand:

```sh
metabib --config metabib.yaml cache \
  --database-dumps /path/to/sql-dumps \
  --archives /path/to/flibusta
```

### Flibusta Script

`scripts/fb2_flibusta.sh` is an example automation script for the common Flibusta
FB2 workflow. It is not required by `metabib`; use it as a starting point for
site-specific scheduling, paths, cleanup, and INPX output choices. The `metabib`
executable is expected to be in the same directory as the script; if
`metabib.yaml` exists there, it is passed to every `metabib` invocation.

Run the full update workflow:

```sh
scripts/fb2_flibusta.sh /volume4/backup/library full mhl
scripts/fb2_flibusta.sh /volume4/backup/library full flib
scripts/fb2_flibusta.sh /volume4/backup/library full both
```

Run indexing only from existing local archives and the latest existing SQL dump
directory matching `<library-root>/flibusta_*`:

```sh
scripts/fb2_flibusta.sh /volume4/backup/library reindex both
```

Both modes accept an optional user account whose home directory should be used as
the working directory. This is useful for Synology Task Scheduler setups:

```sh
scripts/fb2_flibusta.sh /volume4/backup/library full both myuser
```

The `full` mode runs `fetch`, `rollup`, `cache`, `merge`, and the selected INPX
exporter. It exits early when no new daily archives are downloaded or when rollup
does not finalize a new archive. The `reindex` mode skips download and rollup and
reruns `cache`, `merge`, and the selected INPX exporter from already available
data.

The script writes generated INPX files with non-overlapping output prefixes:

- `mhl` mode writes `inpx/flibusta_mhl_<dump-date>.inpx`.
- `flib` mode writes `inpx/flibusta_flib_<dump-date>.inpx` and passes
  `--source-lib flibusta --additional` so FLibrary receives the original source
  library name and additional artifacts are generated next to the INPX.
- `both` mode writes both files from the same merged JSONL input.

In `flib` and `both` modes, additional FLibrary outputs use the same prefix as
the INPX: `flibusta_flib_<dump-date>-annotations.zip` is written when annotation
data is available, and `flibusta_flib_<dump-date>-compilations.zip` is written
when FB2 body fingerprints detect compilations.

After FLibrary additional artifacts are written, the script updates stable links
under `inpx/flib-etc/`: `annotations.zip` points to the current annotations ZIP,
and `compilations.zip` points to the current compilations ZIP when one was
generated. Stale symlinks are removed when an optional artifact is absent.

After a successful download, `full` mode keeps the five newest SQL dump
directories and also keeps the newest SQL dump directory that already contains
`database.manifest.zst`, so reindexing can reuse the latest database cache even
when it is older than the newest downloads.

Expected library layout under `<library-root>`:

- `flibusta/`: finalized local FB2 archives and active `.merging` archive.
- `upd_flibusta/`: downloaded daily update archives.
- `flibusta_<timestamp>/`: downloaded SQL dumps.
- `inpx/`: generated INPX files and merged dataset JSONL artifacts.

The script writes a console log next to itself named like
`flibusta_full_mhl_20260622_103000.log` or
`flibusta_reindex_both_20260622_103000.log`.
For a single combined script and `metabib` debug log, configure logging in the
same-directory `metabib.yaml` like this:

```yaml
logging:
  console:
    level: debug
  file:
    level: none
```

With that configuration, the script log includes phase separators, `metabib`
debug messages, and MariaDB process/client output. The script leaves application
file logging to `metabib` logging configuration.
