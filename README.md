# metabib

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

- `cache` imports SQL dumps, queries database metadata, walks FB2 archive
  entries, parses FB2 descriptions, and writes manifest files for each selected
  source;
- `merge` reads existing manifests and combines database-derived and
  archive-derived metadata into final `metabib.record/1` JSONL records described
  by `docs/metabib.schema.json`;
- future transformation passes can consume the latest JSONL dataset and produce
  derived formats, for example a MyHomeLib-compatible INPX, without coupling the
  main extraction pipeline to legacy output constraints. This may include building
  various update lineages, differential update schemes, etc.

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

## Usage

```sh
metabib cache \
  --database-dumps /path/to/sql-dumps \
  --archives /path/to/flibusta

metabib merge \
  --database-dumps /path/to/sql-dumps \
  --archives /path/to/flibusta \
  --output metabib.jsonl
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

By default, `cache` requires all SQL dump files to report the same dump date
before import. Use `cache --allow-dump-date-mismatch` to accept mixed dump dates;
per-file dump dates are still recorded, while the top-level manifest `dump_date`
is omitted.

Merge from archives only, database only, or both:

```sh
metabib merge --archives /path/to/flibusta --output archive-only.jsonl
metabib merge --database-dumps /path/to/sql-dumps --output database-only.jsonl
metabib merge --database-dumps /path/to/sql-dumps --archives /path/to/flibusta --output combined.jsonl
```

`merge` never starts MariaDB and never reads archives directly. It fails when a
selected manifest is missing, invalid, or stale. Use `--check-md5` for full
source checksum verification, or `--allow-stale` to warn and continue with stale
manifests.

Manifest cache files are zstd-compressed JSONL payloads named `.manifest.zst`,
for example `lib.manifest.zst` or `database.manifest.zst`.

Use global `--verbose` to enable detailed progress reporting.

When `--output-part-size` is used, output files are named
with zero-padded book-id ranges so they sort naturally, for example:

```text
metabib.0000000001-0000120345.jsonl
metabib.0000120346-0000240872.jsonl
```

Dump the default configuration:

```sh
metabib dumpconfig --default metabib.yaml
```
