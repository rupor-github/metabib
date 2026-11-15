# Cache manifests

[User guide](guide.md) · [Merge and inspect datasets](datasets.md)

`cache` extracts source metadata into reusable **manifests**. It does not write
the merged dataset.

## Contents

- [Build manifests](#build-manifests)
- [Reuse and rebuilds](#reuse-and-rebuilds)
- [Archive scope and nested containers](#archive-scope-and-nested-containers)
- [FB2 parsing settings](#fb2-parsing-settings)
- [SQL import and database metadata](#sql-import-and-database-metadata)

## Build manifests

From archives and SQL dumps:

```sh
metabib cache --database-dumps /path/to/sql-dumps --archives /path/to/books
```

From archives alone, without MariaDB:

```sh
metabib cache --archives /path/to/books
```

`--archives` accepts a file or directory and can be repeated. Selecting only
`--database-dumps` builds a database manifest.

To read an already imported database:

```sh
metabib --config metabib.yaml cache --no-import --rebuild --database-dumps /path/to/sql-dumps
```

Configure an existing service or a persistent managed database first.
The default managed database is temporary and starts empty; see
[MariaDB setup](guide.md#set-up-mariadb).

### Command options

| Option | Purpose |
| --- | --- |
| `--archives`, `-a` | Select archive files or directories; repeatable. |
| `--database-dumps` | Select a SQL dump directory. |
| `--rebuild` | Regenerate stale or invalid manifests. |
| `--check-md5` | Verify source MD5 checksums recorded in existing manifests. |
| `--no-import` | Read an existing database instead of importing dumps. |
| `--allow-dump-date-mismatch` | Accept SQL files with different dump dates. |

## Reuse and rebuilds

By default, `cache` builds missing manifests and reuses compatible ones.
Existing manifests are checked for freshness using source modification times;
stale or invalid manifests fail unless `--rebuild` is set.

**`--rebuild` does not force a fresh, compatible manifest to be regenerated.**
To deliberately reparse a particular archive, remove only its manifest and run
`cache --archives /path/to/archive.zip` again.

Manifests are portable between directories and machines. Absolute paths are
retained as provenance, but matching uses file names, recorded metadata,
processing settings, timestamps, and optional MD5 verification.

### Storage

Manifest names end in `.manifest.zst`, for example `lib.manifest.zst` or
`database.manifest.zst`. You can place archive manifests in a central directory:

```yaml
processing:
  manifests:
    archive_dir: "/path/to/library/manifests"
```

When different archives have the same basename, the first keeps the usual
manifest name. Later ones receive source-qualified names, with a warning.

Records are compressed to a temporary Zstandard stream during extraction.
Publication adds a header frame and copies that compressed stream, avoiding an
uncompressed spool and a second compression pass. Standard Zstandard readers
decode the concatenated frames as one JSONL stream.

## Archive scope and nested containers

Each archive manifest has either `fb2` or `usr` scope. Scope is chosen before
validation or extraction:

1. Ignore directories, backup entries such as `.org`, and `.fbd` sidecars when
   classifying the archive.
2. Treat USR-looking paths as `usr`: basenames containing `.usr-` or `.usr.`,
   basenames starting with `usr-`, or parent directory components named `usr`
   or ending in `_usr` or `-usr`.
3. Otherwise, choose `fb2` if there is at least one `.fb2` book entry; choose
   `usr` if there are none.

FB2 entries in a forced-USR archive are ignored. Non-FB2 book entries in an FB2
archive are also ignored. Both cases produce warnings.

USR processing retains non-FB2 entries, pairs supported `.fbd` metadata sidecars,
and can inspect nested book containers when enabled. The final
`Archive manifest created` log includes `usr_fb2_entries_ignored` and
`fb2_non_fb2_entries_ignored` counts. The USR count includes both filename-based
detections such as `.fb2.zip` and nested detections in opaque containers.

### Multi-volume nested archives

Nested multi-volume sets are not extracted across volumes. A complete set becomes
one opaque record for the root volume; continuation parts are skipped.
The record has a `nested_archive_multivolume` issue with `volume_format`,
`volume_count`, `volume_entries`, and `volume_indexes`.

Recognized patterns include:

- RAR: `book.rar` + `book1.rar`;
- RAR: `book.part1.rar` + `book.part2.rar`;
- RAR: `book_partiya_1_.rar` + `book_partiya_2_.rar`;
- 7z: `book.7z.001`, `book.7z.002`, and so on.

Incomplete starts and orphan continuation volumes remain bad nested-archive
issues and produce warnings.

## FB2 parsing settings

### Description size limit

`processing.fb2_max_text_size_mib` defaults to **64 MiB** and must be a positive
integer:

```yaml
processing:
  fb2_max_text_size_mib: 80
```

This limits cumulative text counted while parsing an FB2 book or FBD sidecar,
not total file size. Text accumulated at multiple description-tree levels can
count more than once. The limit also applies to body fingerprints.

Changing the limit does not invalidate existing manifests, including older
ones without this field. Cached parsing failures stay cached until you
deliberately reparse the affected archive.

### Body fingerprints

Enable compact body and section fingerprints with:

```yaml
processing:
  parse_fb2: true
  fb2_body_fingerprints: true
```

Fingerprinting requires FB2 parsing. Different fingerprint settings, models, or
section encodings make archive manifests incompatible and require rebuilding.
FLibrary compilation exports use this evidence.

### Performance

- `processing.archive_content_md5: true` reads and decompresses the entire entry
  even after its FB2 description has been parsed. Turn it off for faster runs
  if full content checksums are unnecessary.
- `processing.fb2_description_tree: false` keeps only `description.title_info`
  rather than the full FB2 description tree.
- Entries are processed in parallel within an archive; archives are processed
  sequentially.
- Global `--verbose` enables detailed progress logs.

### Replacement-only metadata

`processing.fb2_replacement_quality_check` and
`processing.database_replacement_quality_check` default to `true`.
During **merge**, fields whose meaningful characters consist only of Unicode
replacement characters (`�`) are omitted from dataset claims and recorded as
`quality/unicode_replacement_only` issues.

Raw source data in manifests is unchanged. Changing these settings requires
merging again, not rebuilding caches.

## SQL import and database metadata

Current Flibusta and Librusec schemas are detected automatically and recorded in
the database manifest. For Librusec, only tables required for FB2 metadata are
imported; unrelated or unsupported dump files are ignored.

By default, all SQL files must report the same dump date. With
`--allow-dump-date-mismatch`, per-file dates are retained, but the top-level
manifest `dump_date` is omitted.

### Workers and indexes

`database.import_workers` defaults to half the logical CPU count, rounded down,
with a minimum of one. Set it to `1` for sequential imports.
`processing.database_workers` independently controls metadata reads after import.

After a Flibusta import, `cache` adds a `(BookId, nid)` read index to
`libbannotations` unless an index already starts with the full `BookId` column.
This avoids scanning the annotation table for every batch. Index creation and
elapsed time are logged.

### Annotation cleanup

When the optional Flibusta `libbannotations` table exists, manifests keep
annotations ordered by `nid`, including `nid`, `title`, and a plain-text `body`.
Cleanup follows these rules:

- Remove HTML tags, turning block boundaries into whitespace.
- Remove known forum formatting tags while keeping their text; drop media-tag
  contents such as images and videos.
- Keep useful link labels but drop URL targets. For example,
  `[url=http://x]name[/url]` becomes `name`; `[url]http://x[/url]` is dropped.
- Preserve unknown bracketed prose rather than guessing it is markup.
- Omit placeholder-only bodies using `database.annotation_body_placeholders`.
  Matching ignores case and punctuation, so `--skip--` matches `skip`.
- Remove non-whitespace ASCII control characters and collapse whitespace.
- If several annotations survive, omit untitled ones. A single untitled
  annotation is still kept.

When the table is absent, database manifest contents are unchanged.
See [annotation selection during merge](datasets.md#annotations-and-fingerprints)
for how one annotation becomes a dataset claim.

### Author ambiguity

Database manifests record author-name collisions in three scopes: all books,
FB2 only, and USR/non-FB2 only. INPX generators use the scope matching
`--content`, so USR-only collisions do not change FB2 author names.
See [INPX author disambiguation](inpx.md#author-disambiguation).
