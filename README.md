<table>
  <tr>
    <td width="120" valign="middle">
      <img src="docs/library.svg" width="96" alt="metabib" />
    </td>
    <td valign="middle">
      <h1>Metadata extractor from Flibusta/Librusec SQL dumps and FB2 archives.</h1>
    </td>
  </tr>
</table>

## metabib
[![GitHub Release](https://img.shields.io/github/release/rupor-github/metabib.svg)](https://github.com/rupor-github/metabib/releases)

`metabib` extracts metadata from Flibusta/Librusec SQL dumps and FB2 archives into
JSON Lines. It first builds cache manifests for database dumps and/or archives,
then merges those cached artifacts into final JSONL. The project is intended as
a modern replacement for the outdated and cross-platform maintenance-heavy
[InpxCreator](https://github.com/rupor-github/InpxCreator), which depends on an
embedded MySQL library that was dropped by both MySQL and MariaDB many versions
ago.

`metabib` is not trying to reproduce the full `lib2inpx`/`InpxCreator` feature
set. The intentionally unported areas include:

- library content formats other than FB2;
- non-FB2 Librusec update archives such as PDF updates;
- historical dump schema changes and migration compatibility;
- legacy INPX compatibility quirks for specific catalog readers.
- creation of INPX daily updates is not presently supported because the legacy
approach has too many limitations.

Instead, `metabib` aims to provide an easily parsable source of truth for the
growing number of catalog programs. INPX is useful as an interchange artifact,
but it is far from optimal as a primary metadata source: it carries limitations
and assumptions from the program it was originally created for, MyHomeLib,
rather than representing a neutral catalog model.

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

## What It Does

`metabib` is organized around reusable processing passes:

- `fetch` downloads new daily archive updates and SQL dumps from a configured
  remote library profile;
- `rollup` folds daily FB2 update ZIPs into size-bounded local archive ZIPs;
- `cache` imports SQL dumps, queries database metadata, walks FB2 archive
  entries, parses FB2 descriptions, and writes manifest files for each selected
  source;
- `merge` reads existing manifests and combines database-derived and
  archive-derived metadata into one provenance-aware dataset JSONL stream with a
  `metabib.dataset/1` header and `metabib.dataset_record/1` rows;
- `inspect` summarizes and validates merged dataset JSONL or locates individual
  records without producing another artifact;
- `mhl-inpx` consumes the merged dataset JSONL to produce a MyHomeLib-compatible
  FB2 INPX without coupling the main extraction pipeline to legacy output
  constraints;
- `flib-inpx` consumes the same merged dataset JSONL to produce a
  FLibrary-compatible FB2 INPX with extended fields and multiple flat series
  links.

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

### Flibusta Script

`scripts/fb2_flibusta.sh` automates the common Flibusta FB2 workflow. The
`metabib` executable is expected to be in the same directory as the script; if
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
  `--source-lib flibusta` so FLibrary receives the original source library name.
- `both` mode writes both files from the same merged JSONL input.

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
debug messages, and MariaDB process/client output. The script no longer manages
or renames `metabib.log`.

### Fetch Remote Updates

Download new daily archive ZIPs and current SQL dumps using a configured remote
library profile:

```sh
metabib fetch --library flibusta --to upd_flibusta --tosql flibusta_20260622 --continue
metabib fetch --library librusec --to upd_librusec --tosql librusec_20260713 --continue
```

`fetch` replaces the old `libget2` role. It reads profiles from the `fetch`
section of the YAML configuration, discovers the last local book ID from existing
range-named ZIPs in `--to` the same way `libget2` did, downloads only newer daily
archive updates, and decompresses downloaded `*.sql.gz` dumps into `--tosql`.
Both rollup archives such as `fb2-000001-000100.zip` and retained daily updates
such as `f.fb2.000101-000150.zip` count toward the local high-water mark. When
`--tosql` is omitted, the SQL output directory is generated from the library name
and current UTC timestamp. Use `--nosql` to download archive updates only.

The default configuration includes `flibusta` and `librusec` fetch profiles.
Librusec daily FB2 updates use dated names such as
`2026-07-12.818211-818248.503.fb2.zip`; `metabib` downloads only the FB2 update
archives and skips other content formats.

The command preserves the old `libget2` automation exit-code contract: exit code
`0` means no new archive updates were downloaded, exit code `1` means an error
occurred, and exit code `2` means one or more new archive updates were downloaded.
Use code `2` to decide whether archive rollup or index/cache rebuild work is
needed.

Available `fetch` arguments:

- `--library NAME`, `-l NAME`: fetch profile name from configuration. Default is
  `flibusta`.
- `--to DIR`, `-o DIR`: required destination directory for daily archive ZIPs.
- `--tosql DIR`: destination directory for decompressed SQL dump files.
- `--nosql`: skip SQL dump downloads.
- `--retry N`: download attempts per index or file. Default is `3`.
- `--timeout SECONDS`: per-request timeout. Default is `20`.
- `--chunksize MB`: download chunk size used while streaming files. Default is
  `10`.
- `--continue`: resume partial downloads when the server supports ranges.
- `--sticky`: ignore HTTP redirects and keep using the original host.

### Roll Up Daily Archives

Roll downloaded daily update ZIPs into local size-bounded FB2 archives:

```sh
metabib rollup --archives flibusta --updates upd_flibusta
```

`rollup` replaces the old `libmerge` role. It keeps finalized archives and the
active `.merging` archive in `--archives`, reads daily update ZIPs from each
`--updates` directory, and appends ZIP entries without recompressing them. If no
`--updates` directory is provided, `rollup` scans `--archives` for update ZIPs as
well. Generated archive names use the ID width of the existing `.merging` archive
or latest finalized `fb2-*.zip`; new archive directories default to 10-digit IDs.
Daily update ZIPs are always preserved; retention and cleanup are separate
operational concerns.

Rolled-up archive names are always Flibusta-style range names such as
`fb2-0000817672-0000818248.zip`, including when the source updates are dated
Librusec ZIPs.

By default, direct compressed copying does not validate entry payload CRCs. Set
`rollup.validate_crc: true` in the configuration to decompress each non-empty
numeric entry for CRC-32 validation before copying it. Validation can significantly
reduce performance, but entries are still copied in their original compressed form
without recompression.

The command preserves the old `libmerge` automation exit-code contract: exit code
`0` means no finalized archive was produced, exit code `1` means an error
occurred, and exit code `2` means one or more finalized `fb2-*.zip` archives were
created. Use code `2` to decide whether cache/index rebuild work is needed.

Available `rollup` arguments:

- `--archives DIR`, `-a DIR`: required directory for finalized `fb2-*.zip`
  archives and the active `fb2-*.merging` archive.
- `--updates DIR`, `-u DIR`: directory containing daily update ZIPs; can be
  repeated. Defaults to `--archives` when omitted.
- `--size MB`: finalized archive target size in decimal megabytes. Default is
  `2000`.

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

By default, `cache` requires all SQL dump files to report the same dump date
before import. Use `cache --allow-dump-date-mismatch` to accept mixed dump dates;
per-file dump dates are still recorded, while the top-level manifest `dump_date`
is omitted.

For current Librusec dumps, only the tables required for FB2 metadata are
imported. Unsupported or unrelated dump files in the SQL directory are ignored by
the importer.

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
source checksum verification, or `--allow-stale` to warn and continue with stale
manifests.

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
following value is a dataset record (`metabib.dataset_record/1`). Legacy
`metabib.record/1` merged JSONL input is rejected by INPX generation.

Archive records are anchored by dataset archive ordinal and ZIP entry index.
Physical record order is never inferred from entry filenames or database book IDs.
When database enrichment is enabled, merge first treats a positive numeric entry
stem as matching evidence, then tries a unique database filename alias if no
numeric database row exists. The selected database observation records the match
method, and conflicting numeric/filename evidence is retained as a structured
issue instead of changing the physical record locator.

Examples:

- `42.fb2` first tries database book `42` through `numeric_entry_stem`; if present,
  database and FB2 claims share one archive-entry record while the archive locator
  remains the ZIP entry position.
- `Some.Book.fb2` can match a unique database filename alias; the database book ID
  becomes a catalog identity claim, not the physical record position.
- `notes.fb2` with no database match remains a valid archive/FB2-only record and
  has no invented catalog identity.

### Inspect Dataset

Use `inspect` for quick checks and debugging of merged dataset JSONL artifacts:

```sh
metabib inspect --input combined
metabib inspect --input combined --archives
metabib inspect --input combined --validate
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

Available modes and options:

- `--archives`: list archive source IDs, ordinals, entry counts, names, and path
  hints. IDs such as `archive-0001` can then be used with `--archive`.
- `--validate`: stream the full dataset and validate ordering, schemas,
  provenance references, source declarations, and archive indexes without
  writing any derived artifact. Successful output includes the number of records
  read.
- `--book-id ID`: return the first record matching a primary locator, Flibusta
  catalog identity, or database observation with that book ID.
- `--archive ID --index INDEX`: return the record at the zero-based entry index
  in the specified dataset archive source.
- `--file NAME`: return the first record whose artifact name or archive occurrence
  entry matches `NAME`; matching is case-insensitive.
- `--json`: emit the selected summary, archive list, validation result, or record
  as machine-readable JSON.

Only one of `--archives`, `--book-id`, `--archive`/`--index`, and `--file` may be
used at a time. `--validate` cannot be combined with any of those modes, while
`--json` can be used with every mode. A lookup that finds no matching record exits
with status `4`; other failures use status `1`.

Archive source IDs are local to one merged dataset. Use archive names, path hints,
and checksums for long-term correlation across regenerated datasets.

### INPX Generation

Build a MyHomeLib-compatible FB2 INPX from merged dataset JSONL:

```sh
metabib mhl-inpx --input all --output flibusta
```

`mhl-inpx` is intentionally FB2-only. It consumes the merged dataset JSONL; it
does not read SQL dumps, start MariaDB, or parse FB2 archives directly. Database
and FB2 metadata are read from normalized v2 claims.

When the merge input is database-only and has no archives, `mhl-inpx` writes the
records into `online.inp`, matching the legacy `lib2inpx` database-only output.
When archive metadata is present, `online.inp` is not created; archive-less
records are ignored to preserve the historical archive-based behavior.

Available `mhl-inpx` arguments:

- `--input PREFIX`, `-i PREFIX`: required input prefix or exact dataset path.
  `metabib mhl-inpx --input all` discovers exactly one of `all.jsonl`,
  `all.jsonl.zst`, `all.jsonl.gz`, or `all.jsonl.zip`.
- `--output PREFIX`, `-o PREFIX`: required output prefix. The dump date from the
  dataset header is appended automatically, so `--output flibusta` writes a file
  named like `flibusta_20260603.inpx`.
- `--format MODE`: INPX record layout. Supported values are `2x` and `ruks`.
  Default is `2x`, matching the classic MyHomeLib/lib2inpx format. `ruks` appends
  MD5 and replacement fields when available.
- `--sequence MODE`: database sequence selection. Supported values are `author`,
  `publisher`, and `ignore`. Default is `author`, matching lib2inpx FB2 mode.
- `--prefer-fb2 MODE`: how FB2 metadata is used relative to database metadata for
  authors and sequences. Supported values are `ignore`, `merge`, `complement`,
  and `replace`. Default is `complement`, matching the historical Flibusta script:
  database authors and sequence data are preferred when present, and FB2 metadata
  fills missing values. Use `replace` when FB2 author order should win.

INPX-specific defaults live under the `inpx` section of the YAML configuration.
They include MyHomeLib field length limits and the `collection.info` template.
The default template is lib2inpx-compatible.

```yaml
inpx:
  quick_fix: true
  disambiguate_authors: true
  comment_template: "\ufeff{{ .DatabaseName }} FB2 - {{ .DisplayDate }}\r\n{{ .DatabaseName }}_{{ .DumpDate }}\r\n65536\r\nЛокальные архивы библиотеки {{ .DatabaseName }} (FB2) {{ .DisplayDate }}"
  version_template: "{{ .DumpDate }}\r\n"
```

When `disambiguate_authors` is enabled, database cache manifest creation records
DB authors whose cleansed, non-truncated `LastName,FirstName,MiddleName` value
collides with a different database contributor ID. INPX generation uses that
metadata to append a stable suffix to the exported last-name field for both
Flibusta and Librusec datasets. FB2-only authors are not changed. Existing
database manifests without this optional metadata behave as before; rebuild the
database cache to populate it.

`comment_template` and `version_template` are Go `text/template` values rendered
when `collection.info` and `version.info` are written. Available values are
`.DatabaseName`, `.DumpDate`, and `.DisplayDate`; slim-sprig template functions
are available. If you replace the collection template and still need
MyHomeLib/lib2inpx compatibility, keep the leading `\ufeff` BOM.

Build a FLibrary-compatible FB2 INPX from the same merged dataset JSONL:

```sh
metabib flib-inpx --input all --output flibusta
```

`flib-inpx` is also FB2-only and consumes only merged dataset JSONL. It does not
read SQL dumps or archives directly. Unlike `mhl-inpx`, it emits no dummy records
and always writes `structure.info` with FLibrary extensions such as `FOLDER`,
`YEAR`, and `SOURCELIB`.

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

Both INPX generators canonicalize language values at generation time by default.
Raw merged dataset JSONL remains unchanged. Database language still wins over FB2
language, except ignored placeholder values are treated as absent so FB2 can be
used as a fallback. Resolved output is the base language subtag, so values such as
`RU`, `en-US`, and `sr-Latn` become `ru`, `en`, and `sr`. Explicit aliases handle
known noisy values such as `sp -> es`, `gr -> el`, and `un -> und`.

The shared resolver is configured under `inpx.language`:

```yaml
inpx:
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

`aliases` are matched case-insensitively after whitespace collapse. Full-value
aliases are checked before comma splitting, so phrase aliases containing commas
are supported. If no full-value alias matches, the raw language value is split on
commas and parts are tried from left to right until one resolves. `fallback_locales`
are BCP 47 display locales used for localized language-name matching after
same-record context does not match. `ignore_patterns` are regular expressions for
values that should be skipped entirely. `context_rules` apply only when another
source-language observation on the same record supports the correction.

Unresolved canonicalization attempts are emitted raw for compatibility and always
logged as warnings with book ID, source field, observation, original value,
candidate value, locator, artifact, source languages, and context language. With
global `--verbose`, ignored values and successful canonicalizations that change
the output are logged with the same identifying details.

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
