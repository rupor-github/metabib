# User guide

[README](../README.md) · [Command reference](#command-reference)

`metabib` turns SQL dumps and book archives into a reusable metadata dataset.
You can use that dataset directly or export an INPX catalog for a library reader.
It does not convert books.

## Contents

- [Quick start](#quick-start)
- [How the workflow fits together](#how-the-workflow-fits-together)
- [Set up MariaDB](#set-up-mariadb)
- [Configuration](#configuration)
- [Keep a library up to date](#keep-a-library-up-to-date)
- [Choose an INPX format](#choose-an-inpx-format)
- [Check results and troubleshoot](#check-results-and-troubleshoot)
- [Command reference](#command-reference)

## Quick start

Already have local book archives? Start with an archive-only dataset.
**MariaDB is not needed for this example.**

Replace `/path/to/books` with your archive directory. Examples use `metabib`
from `PATH`; use `./metabib` on Unix or `.\metabib.exe` in PowerShell if you
unpacked it into the current directory.

### 1. Extract metadata

```sh
metabib cache --archives /path/to/books
```

This creates reusable cache files called **manifests**. The first run can take
time because it reads the archives; later runs reuse compatible manifests.

### 2. Create a dataset

```sh
metabib merge --archives /path/to/books --output library
```

The result is one compressed JSON Lines file: `library.jsonl.zst`.
`library` is an output prefix, not a directory or a complete file name.

### 3. Check it

```sh
metabib inspect --input library
metabib inspect --input library --validate
```

The first command shows a summary. The second reads and validates the full
dataset.

### 4. Export an INPX catalog (optional)

For MyHomeLib:

```sh
metabib mhl-inpx --input library --output my-library
```

For FLibrary:

```sh
metabib flib-inpx --input library --output my-library
```

The exporter adds a date to the INPX file name. Both commands select FB2 books
by default; add `--content all` to include non-FB2 books.

### Add SQL metadata

For database titles, authors, genres, and other catalog metadata, first
[set up MariaDB](#set-up-mariadb), then select both sources in **both** steps:

```sh
metabib cache --database-dumps /path/to/sql-dumps --archives /path/to/books
metabib merge --database-dumps /path/to/sql-dumps --archives /path/to/books --output library
```

Current Flibusta and Librusec dump schemas are detected automatically.
You can also select only `--database-dumps` for a database-only dataset.
For Librusec labeling, see [Library name](#library-name).

## How the workflow fits together

```text
SQL dumps ─────┐
               ├── cache ── manifests ── merge ── dataset ── INPX export
Book archives ─┘                                     │
                                              inspect / trace
```

| Step | What it does | When to run it |
| --- | --- | --- |
| `fetch` | Downloads daily updates and SQL dumps. | When you want remote updates. |
| `rollup` | Combines daily ZIPs into local FB2 and USR archives. | After downloading updates. |
| `cache` | Reads selected sources and saves manifests. | For new or changed sources. |
| `merge` | Combines existing manifests into one dataset. | After cache changes, or to change merge settings. |
| `inspect` / `trace` | Checks results or explains their origin. | Whenever you need to investigate. |
| INPX export | Makes a reader-specific catalog from the dataset. | Whenever you need a new catalog. |

**FB2** means FictionBook content. **USR** is the non-FB2 collection, such as
EPUB, PDF, and DJVU. Their updates and archives are tracked separately.

Only the SQL part of `cache` needs MariaDB. `merge` does not reparse archives
or query a database, and INPX export reads only the merged dataset.

Keep manifests: they let you change merge or export options without repeating
expensive extraction. See [cache behavior](cache.md#reuse-and-rebuilds) before
deleting or rebuilding them.

## Set up MariaDB

Skip this section if you only process archives.

By default, `metabib` starts a **private MariaDB instance** for SQL extraction
and stops it afterward. You do not need a running system database service.

### Make the binaries available

`metabib` searches `./mariadb` recursively under the **startup working directory**,
then searches `PATH`. You can also configure explicit paths.

| Purpose | Accepted binary names |
| --- | --- |
| Server | `mariadbd` or `mysqld` |
| Database initializer | `mariadb-install-db` or `mysql_install_db` |
| SQL import client | `mariadb` or `mysql` |
| Shutdown client (optional) | `mariadb-admin` or `mysqladmin` |

Windows binaries have the `.exe` suffix. Without the optional shutdown client,
`metabib` falls back to signaling its private server process.

**Windows:** download a ZIP package from [MariaDB](https://mariadb.org/download/),
unpack it under `mariadb/`, and run `metabib` from the directory containing that
folder.

**Linux:** use a MariaDB tarball or install your distribution's package. On
Debian/Ubuntu, for example:

```sh
sudo apt install mariadb-server
```

Managed mode does not use the system service installed by that package. If you
do not need that service for anything else, you can disable its automatic startup:

```sh
sudo systemctl disable mariadb
```

This does not stop an already-running service.

**macOS and other systems:** install suitable MariaDB binaries and make them
available through `PATH` or explicit configuration paths.

**Synology:** install the `MariaDB 10` package and configure the paths for your
device. The following paths are placeholders, not a fixed Synology layout:

```yaml
version: 1
processing:
  manifests:
    archive_dir: "/path/to/library/manifests"
database:
  server_path: "/path/to/MariaDB10/bin/mariadbd"
  install_db_path: "/path/to/MariaDB10/bin/mariadb-install-db"
  client_path: "/path/to/MariaDB10/bin/mariadb"
  admin_path: "/path/to/MariaDB10/bin/mariadb-admin"
```

### Temporary or persistent database?

The default is a fresh database in the OS temporary directory, removed on
shutdown:

```yaml
database:
  managed: true
  temporary: true
```

Set `temporary: false` to reuse `database.data_dir` between runs. Its default
location is `data/mariadb` under the executable directory.

Allow enough disk space for the SQL files, imported database, manifests, and
dataset. In temporary mode, the imported database uses the OS temporary
filesystem, which may have less free space than your library disk.

### Use an existing service

Set a DSN in your configuration to use an existing MariaDB service instead:

```yaml
version: 1
database:
  dsn: "user:password@tcp(127.0.0.1:3306)/flibusta"
```

Alternatively, set `database.managed: false` and configure the connection fields
shown by `dumpconfig`. Importing SQL dumps still needs the `mariadb`/`mysql`
command-line client. Use a database dedicated to this workflow.

To skip import and read a database you already populated, use `cache --no-import`
with `--database-dumps`. This needs an existing service or a persistent managed
database, not the default fresh temporary instance.

### Tune the managed server

Extra server options go in `database.server_args`, one argument per item:

```yaml
database:
  server_args:
    - "--innodb-buffer-pool-size=1G"
    - "--key-buffer-size=128M"
```

Arguments are passed without shell expansion. They precede metabib's generated
arguments, so metabib's datadir, socket, and other duplicate settings take
precedence. These options affect only the managed server, not the initializer
or an external service.

## Configuration

You can run with defaults or generate a commented configuration file:

```sh
metabib dumpconfig --default metabib.yaml
```

Edit the settings you need, then pass the file **before the subcommand**:

```sh
metabib --config metabib.yaml cache --archives /path/to/books
```

Unknown YAML fields are rejected. Omitted fields keep their defaults, so a
configuration file can contain just the settings you want to change.

To see the effective configuration after loading your file:

```sh
metabib --config metabib.yaml dumpconfig effective.yaml
```

Omit the destination to print YAML to stdout. `dumpconfig --default` always uses
the embedded defaults, not a loaded config file. Runtime values such as paths,
worker counts, and Windows ports are expanded when the configuration is generated.

### Library name

`database.name` also supplies the library label stored in datasets and used by
INPX metadata. Its default is `flibusta`. To label a Librusec collection, set it
before caching and merging:

```yaml
version: 1
database:
  name: librusec
```

Pass that configuration to `cache` and `merge` with `--config`.
This setting also names the managed SQL database. Selecting `fetch --library librusec`
or autodetecting a Librusec SQL schema does **not** change it.

## Keep a library up to date

For an existing local Flibusta FB2 collection:

```sh
metabib fetch --library flibusta --to /path/to/updates --tosql /path/to/sql-dumps --continue
metabib rollup --archives /path/to/books --updates /path/to/updates
metabib cache --rebuild --database-dumps /path/to/sql-dumps --archives /path/to/books
metabib merge --database-dumps /path/to/sql-dumps --archives /path/to/books --output library
metabib mhl-inpx --input library --output my-library
```

Run these as separate commands when trying the workflow interactively.
**`fetch` and `rollup` use exit code `2` for successful work**, not failure:
fetch downloaded new archives, or rollup finalized an archive. Do not chain
these commands with `&&` or use `set -e` without handling that status.

`fetch` downloads daily updates, not a complete historical archive collection.
`rollup` keeps small pending updates in `.merging` files until its finalization
policy is reached. See [downloads and rollup](updates.md) for profiles, resume
behavior, and finalization policies.

For scheduled Flibusta FB2 updates, adapt the supplied
[automation script](scripts.md#flibusta-script).

## Choose an INPX format

| Your goal | Command | Default content |
| --- | --- | --- |
| MyHomeLib catalog | `mhl-inpx` | FB2 |
| FLibrary catalog with multiple series links | `flib-inpx` | FB2 |
| Filtered catalog or multiple `.inp` groups | `inpx` | All |

`inpx` requires archive-backed records. The other two also support database-only
datasets.

To prefer FB2 authors over database authors, use `--prefer-fb2 replace`.
The default, `complement`, prefers database authors; `merge` does **not**
concatenate the two author lists.

See the [INPX reference](inpx.md) for source preferences, series, language
normalization, author disambiguation, and filtering examples.

## Check results and troubleshoot

| What you need | Command or action |
| --- | --- |
| Dataset summary | `metabib inspect --input library` |
| Full validation | `metabib inspect --input library --validate` |
| Records with reported issues | `metabib inspect --input library --issues` |
| Find a book | `metabib inspect --input library --book-id 12345` |
| Explain where a book's metadata came from | `metabib trace --input library --book-id 12345` |
| More progress detail | Put global `--verbose` before the subcommand. |
| Missing or stale manifests | Run `cache` for the same sources; use `--rebuild` for stale/invalid caches. |
| Ambiguous input prefix | Pass the exact dataset file name instead. |

`inspect` reports disagreements as well as parsing problems. An issue does not
necessarily mean a record is unusable. See [dataset diagnostics](datasets.md).

For faster archive extraction, consider disabling
`processing.archive_content_md5` if you do not need content checksums, and
`processing.fb2_description_tree` if you only need `title_info`. Full content
MD5 requires reading and decompressing the entire entry.

## Command reference

- [Downloads and archive rollup](updates.md)
- [Cache manifests and extraction settings](cache.md)
- [Datasets: merge, inspect, and trace](datasets.md)
- [INPX generation and templates](inpx.md)
- [Automation and repair scripts](scripts.md)
- [JSON schemas and versions](schemas.md)

For the current command-line options, run `metabib --help` or
`metabib <command> --help`.
