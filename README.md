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

`metabib` builds a searchable, reusable metadata dataset from SQL dumps and book
archives. Use the JSON Lines dataset in your own tools, or export an INPX catalog
for MyHomeLib or FLibrary.

Extraction is cached in **manifests**, so you can update database metadata or
change export options without parsing the whole archive collection again.

## What It Does

| Task | Commands |
| --- | --- |
| Download daily updates and SQL dumps | `fetch` |
| Combine daily ZIPs into local archives | `rollup` |
| Extract and cache source metadata | `cache` |
| Combine cached metadata into one JSONL dataset | `merge` |
| Validate records and trace their sources | `inspect`, `trace` |
| Export reader-specific or filtered catalogs | `mhl-inpx`, `flib-inpx`, `inpx` |

Supports current Flibusta and Librusec SQL schemas, FB2 archives, and USR
(non-FB2) collections such as EPUB, PDF, and DJVU. FB2 descriptions and supported
FBD sidecars enrich the catalog when available.

## System Requirements

Available operating system and processor builds are listed on the
[releases page](https://github.com/rupor-github/metabib/releases).
These platforms are supported to the extent supported by the Go runtime used
to build each release. See Go's
[minimum requirements](https://go.dev/wiki/MinimumRequirements) for operating
system and hardware requirements.

Prebuilt `metabib` binaries are self-contained; **Go does not need to be installed**.
SQL dump extraction additionally needs MariaDB:

| Workflow | MariaDB requirement |
| --- | --- |
| Archive-only extraction, downloads, rollup, merge, inspection, INPX export | None |
| SQL extraction with a private managed database (default) | MariaDB server, initializer, and command-line client |
| SQL extraction using an existing service | Access to that service; command-line client if importing dumps |

MariaDB is not bundled. Suitable binaries must be available for your platform;
see [MariaDB setup](docs/guide.md#set-up-mariadb).
Allow disk space for your archives, extracted SQL, imported database, caches,
and generated dataset. The default temporary database uses the OS temp directory.

## Installation

Download the matching archive from the
[releases page](https://github.com/rupor-github/metabib/releases) and unpack it
in a convenient location. Add that directory to `PATH`, or run the executable
by its path.

Start with the [quick start](docs/guide.md#quick-start). It builds an archive-only
dataset without MariaDB, then shows how to add SQL metadata and export INPX.

## Documentation

- **[User guide](docs/guide.md)** — setup, first dataset, updates, and troubleshooting.
- [Downloads and rollup](docs/updates.md) — profiles, resume, archive finalization.
- [Cache manifests](docs/cache.md) — extraction, reuse, and parsing settings.
- [Datasets](docs/datasets.md) — merge, validation, matching, and provenance.
- [INPX generation](docs/inpx.md) — formats, metadata preferences, filters, and templates.
- [Scripts](docs/scripts.md) — scheduled updates and archive repair helpers.
- [JSON schemas](docs/schemas.md) — data formats and current versions.

## Project Scope

The primary output is a neutral, provenance-aware metadata dataset. INPX is an
optional interchange format, not the underlying catalog model.

The project focuses on current Flibusta/Librusec workflows. It does not support
unrelated dump schemas, INPX daily updates, or reader-specific quirks outside
the generated formats.

## Contributing

For bug reports, feature requests, and pull requests, please read the
[contribution guidelines](CONTRIBUTING.md).
