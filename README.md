# metabib

`metabib` extracts metadata from Flibusta-style SQL dumps and FB2 archives into
JSON Lines. It first builds cache manifests for database dumps and/or archives,
then merges those cached artifacts into final JSONL.

## Current Scope

- discovers MariaDB binaries in the current directory, `./bin`, subdirectories,
  or `PATH`;
- starts a private socket-only MariaDB server by default, creating a local
  datadir automatically when needed;
- imports `*.sql` dumps with the discovered `mariadb` or `mysql` client;
- queries the imported MariaDB database for book, author, translator, genre,
  sequence, rating, filename, joined-book, and recommendation metadata;
- walks provided `.zip` archives or archive directories;
- parses FB2 `<description>/<title-info>` metadata while preserving the full
  parsed `<description>` tree;
- writes `metabib.record/1` JSONL records described by
  `docs/metabib.schema.json`.

By default, `metabib` starts and stops a private local MariaDB instance using
discovered MariaDB binaries. It does not require a system database service. To
use an existing service instead, pass `--db-dsn` or `--db-use-service`.

## Usage

```sh
metabib cache --rebuild \
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
metabib cache --rebuild --database-dumps /path/to/sql-dumps \
  --db-dsn 'user:password@tcp(127.0.0.1:3306)/flibusta'
```

Build only archive manifests without starting MariaDB:

```sh
metabib cache --rebuild --archives /path/to/flibusta
```

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

When `output.part_size` or `--output-part-size` is used, output files are named
with zero-padded book-id ranges so they sort naturally, for example:

```text
metabib.0000000001-0000120345.jsonl
metabib.0000120346-0000240872.jsonl
```

Dump the default configuration:

```sh
metabib dumpconfig --default metabib.yaml
```

Build tasks follow the `fb2cng` style:

```sh
go tool task
go tool task test
go tool task release
```
