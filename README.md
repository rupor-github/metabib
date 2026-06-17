# metabib

`metabib` extracts metadata from Flibusta-style SQL dumps and FB2 archives into
JSON Lines. Each output record keeps database and FB2 metadata in separate
source sections so later tools can decide how to merge or prefer fields when
producing INPX or other formats.

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
metabib build /path/to/sql-dumps \
  --archive /path/to/flibusta \
  --output metabib.jsonl
```

To use an already imported database:

```sh
metabib build --no-import --archive /path/to/flibusta --output metabib.jsonl
```

Force a clean managed database rebuild:

```sh
metabib build /path/to/sql-dumps --archive /path/to/flibusta --db-overwrite
```

Use an existing MariaDB service instead of a managed one:

```sh
metabib build /path/to/sql-dumps --archive /path/to/flibusta \
  --db-dsn 'user:password@tcp(127.0.0.1:3306)/flibusta'
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
