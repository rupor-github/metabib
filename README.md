<h1>
    <img src="docs/library.svg" style="vertical-align:middle; width:14%" align="absmiddle"/>
    <span style="vertical-align:middle;">&nbsp;&nbsp;Metadata extractor from Flibusta-style SQL dumps and FB2 archives.</span>
</h1>

## metabib
[![GitHub Release](https://img.shields.io/github/release/rupor-github/metabib.svg)](https://github.com/rupor-github/metabib/releases)

`metabib` extracts metadata from Flibusta-style SQL dumps and FB2 archives into
JSON Lines. It first builds cache manifests for database dumps and/or archives,
then merges those cached artifacts into final JSONL. The project is intended as
a modern replacement for the outdated and cross-platform maintenance-heavy
[InpxCreator](https://github.com/rupor-github/InpxCreator), which depends on an
embedded MySQL library that was dropped by both MySQL and MariaDB many versions
ago.

`metabib` is not trying to reproduce the full `lib2inpx`/InpxCreator feature
set. The intentionally unported areas include:

- library content formats other than FB2;
- Librusec schema differences and Flibusta-specific assumptions;
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

## What It Does

`metabib` is organized around reusable processing passes:

- `fetch` downloads new daily archive updates and SQL dumps from a configured
  remote library profile;
- `rollup` folds daily FB2 update ZIPs into size-bounded local archive ZIPs;
- `cache` imports SQL dumps, queries database metadata, walks FB2 archive
  entries, parses FB2 descriptions, and writes manifest files for each selected
  source;
- `merge` reads existing manifests and combines database-derived and
  archive-derived metadata into final `metabib.record/1` JSONL records described
  by `docs/metabib.schema.json`;
- `mhl-inpx` consumes the merged JSONL dataset and metadata sidecar to produce a
  MyHomeLib-compatible FB2 INPX without coupling the main extraction pipeline to
  legacy output constraints.

The same transformation approach can support other derived artifacts later,
including update lineages and differential update schemes.

## MariaDB Binaries

When the cache pass processes SQL dumps in the default managed mode, `metabib`
discovers MariaDB binaries recursively in `./mariadb` first, then in `PATH`,
starts a private local MariaDB server, imports `*.sql` dumps with the discovered
`mariadb` or `mysql` client, and stops the server when processing is done. It
does not require a system database service.

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
`mariadb-install-db.exe`, and `mariadb.exe` from that tree automatically.

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
```

## Usage

### Flibusta Script

`scripts/fb2_flibusta.sh` automates the common Flibusta FB2 workflow. The
`metabib` executable is expected to be in the same directory as the script; if
`metabib.yaml` exists there, it is passed to every `metabib` invocation.

Run the full update workflow:

```sh
scripts/fb2_flibusta.sh /volume4/backup/library full
```

Run indexing only from existing local archives and the latest existing SQL dump
directory matching `<library-root>/flibusta_*`:

```sh
scripts/fb2_flibusta.sh /volume4/backup/library reindex
```

Both modes accept an optional third argument with the user account whose home
directory should be used as the working directory. This is useful for Synology
Task Scheduler setups:

```sh
scripts/fb2_flibusta.sh /volume4/backup/library full myuser
```

The `full` mode runs `fetch`, `rollup`, `cache`, `merge`, and `mhl-inpx`. It exits
early when no new daily archives are downloaded or when rollup does not finalize a
new archive. The `reindex` mode skips download and rollup and reruns `cache`,
`merge`, and `mhl-inpx` from already available data.

Expected library layout under `<library-root>`:

- `flibusta/`: finalized local FB2 archives and active `.merging` archive.
- `upd_flibusta/`: downloaded daily update archives.
- `flibusta_<timestamp>/`: downloaded SQL dumps.
- `inpx/`: generated INPX files and merged JSONL sidecars/parts.

The script writes a console log next to itself named like
`flibusta_full_20260622_103000.log` or `flibusta_reindex_20260622_103000.log`.
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
```

`fetch` replaces the old `libget2` role. It reads profiles from the `fetch`
section of the YAML configuration, discovers the last local book ID from existing
`fb2-*.zip` or `fb2-*.merging` archives in `--to`, downloads only newer daily
archive updates, and decompresses downloaded `*.sql.gz` dumps into `--tosql`.
When `--tosql` is omitted, the SQL output directory is generated from the library
name and current UTC timestamp. Use `--nosql` to download archive updates only.

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
metabib rollup --archives flibusta --updates upd_flibusta --keep-updates
```

`rollup` replaces the old `libmerge` role. It keeps finalized archives and the
active `.merging` archive in `--archives`, reads daily update ZIPs from each
`--updates` directory, and appends ZIP entries without recompressing them. If no
`--updates` directory is provided, `rollup` scans `--archives` for update ZIPs as
well. Generated archive names use the ID width of the existing `.merging` archive
or latest finalized `fb2-*.zip`; new archive directories default to 10-digit IDs.

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
- `--keep-updates`: keep consumed daily update ZIPs instead of removing them.

### Build Cache Manifests

```sh
metabib cache \
  --database-dumps /path/to/sql-dumps \
  --archives /path/to/flibusta

metabib merge \
  --database-dumps /path/to/sql-dumps \
  --archives /path/to/flibusta \
  --output metabib
```

To use an already imported database:

```sh
metabib cache --rebuild --no-import --database-dumps /path/to/sql-dumps
```

Force a clean managed database rebuild:

```sh
metabib cache --rebuild --database-dumps /path/to/sql-dumps --db-overwrite
```

Use an existing MariaDB service instead of a managed one:

```sh
metabib cache --rebuild --database-dumps /path/to/sql-dumps --config metabib.yaml
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

Merged JSONL output is zstd-compressed by default, using the same compression
level as manifest files. Use `--output-compression zstd`, `gz`, `zip`, or `none`
to select a different output container. The `--output` value is an output prefix,
not a final file name: `metabib merge --output all` writes files named like
`all.<bookid_start>-<bookid_end>.jsonl.zst`. Existing output files are replaced;
when that happens, `metabib` logs an overwrite warning.

`merge` also writes a metadata sidecar using the same compression mode, for
example `all.meta.json.zst`. It records the database dump date and archive entry
layout needed for exact MyHomeLib INPX generation.

### INPX Generation

Build a MyHomeLib-compatible FB2 INPX from merged JSONL parts:

```sh
metabib mhl-inpx --input all --output flibusta
```

`mhl-inpx` is intentionally FB2-only. It consumes the merged JSONL dataset and the
merge metadata sidecar; it does not read SQL dumps, start MariaDB, or parse FB2
archives directly. FB2 fallback metadata is read from
`sources.fb2.description.title_info`.

Available `mhl-inpx` arguments:

- `--input PREFIX`, `-i PREFIX`: required input prefix. `metabib mhl-inpx --input all`
  discovers one `all.meta.json*` sidecar and all matching `all.*.jsonl*` parts,
  including uncompressed, zstd, gzip, and ZIP-compressed merge outputs.
- `--output PREFIX`, `-o PREFIX`: required output prefix. The dump date from merge
  metadata is appended automatically, so `--output flibusta` writes a file named
  like `flibusta_20260603.inpx`.
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
  comment_template: "\ufeff%s FB2 - %s\r\n%s\r\n65536\r\nЛокальные архивы библиотеки %s (FB2) %s"
```

Template arguments are library name, display date, generated collection name,
library name, and display date. If you replace the template and still need
MyHomeLib/lib2inpx compatibility, keep the leading `\ufeff` BOM.

Existing INPX output is replaced only after the new archive is fully written. If
an existing file is overwritten, `metabib` logs a warning. During generation,
`metabib` logs the selected metadata, input part count, record loading progress,
one live message per created `.inp` member, and final aggregate INPX statistics.

Manifest cache files are zstd-compressed JSONL payloads named `.manifest.zst`,
for example `lib.manifest.zst` or `database.manifest.zst`. When archive
manifests are stored in a central `processing.manifests.archive_dir`, the first
archive with a basename keeps the usual manifest name. Later archives with the
same basename get a source-qualified manifest name and a warning is logged.

Use global `--verbose` to enable detailed progress reporting.

When `--output-part-size` is used, output files are named
with zero-padded book-id ranges so they sort naturally, for example:

```text
metabib.0000000001-0000120345.jsonl.zst
metabib.0000120346-0000240872.jsonl.zst
```

Dump the default configuration:

```sh
metabib dumpconfig --default metabib.yaml
```
