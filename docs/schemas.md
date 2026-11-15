# JSON schemas

[User guide](guide.md) · [Dataset reference](datasets.md)

`metabib` writes JSON Lines: one JSON value per line. A merged dataset starts
with a header, followed by book records. Cache manifests also have a header and
record stream, but use different schemas; they are not interchangeable with
merged datasets.

## Schema files

| Data | Schema |
| --- | --- |
| Dataset header | [`metabib-dataset.schema.json`](metabib-dataset.schema.json) |
| Dataset records | [`metabib-dataset-record.schema.json`](metabib-dataset-record.schema.json) |
| Source cache records | [`metabib.schema.json`](metabib.schema.json) |
| Archive manifest header | [`metabib-archive-manifest.schema.json`](metabib-archive-manifest.schema.json) |
| Database manifest header | [`metabib-database-manifest.schema.json`](metabib-database-manifest.schema.json) |

## Current versions

| Data | Version |
| --- | --- |
| Merged dataset header | `metabib.dataset/1` |
| Merged dataset record | `metabib.dataset_record/1` |
| Manifest payload record | `metabib.record/2` |
| Generated archive manifest header | `metabib.archive_manifest/2` |
| Database manifest header | `metabib.database_manifest/2` |

Archive manifest version `2` adds `scope`, with values `fb2` or `usr`.
Legacy `metabib.archive_manifest/1` headers are still accepted as FB2 manifests,
so compatible existing caches do not need an expensive rebuild.

## Read and validate

Merged output is one artifact: `.jsonl`, `.jsonl.zst`, `.jsonl.gz`, or
`.jsonl.zip`. Zstandard is the default. INPX commands require a merged dataset,
not raw `metabib.record/*` cache records.

```sh
metabib inspect --input library.jsonl.zst --validate
```

Validation checks ordering, schemas, provenance references, declared sources,
and archive indexes. See [inspect](datasets.md#inspect-a-dataset) for details.
