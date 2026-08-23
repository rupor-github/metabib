# Non-FB2 Archive Processing

## Goal

Expand `metabib` cache and merge processing to cover non-FB2 books transparently, without new command-line arguments.

Scope for first pass:

- Include non-FB2 archive entries in archive manifests.
- Include non-FB2 database rows in database manifests.
- Leave mixed FB2/non-FB2 archives out of first-pass scope so existing FB2 archive manifests remain reusable.
- Support USR archives such as `/mnt/grumpy/library/flibusta_usr`.
- Use `.fbd` sidecars as metadata when they can be paired with a book.
- Keep fetch and rollup commands unchanged.
- Do not add collision fingerprinting for non-FB2 entries.
- Add detailed debug/warn logging because matching and sidecar behavior is complex.
- Report DB match statistics during merge, not during archive cache creation.

## Current Behavior

Current archive processing emits only `.fb2` entries. Unmatched FB2 archive entries are still emitted as records; after merge they have `database.present=false`. If FB2 parsing is enabled, FB2 metadata can still provide claims.

USR entries should follow the same rule: unmatched non-FB2 archive entries still become archive records with `database.present=false`.

Current database manifest emits only `libbook` rows where `FileType = 'fb2'`. This must change, otherwise merge cannot attach DB info to non-FB2 archive records.

## InpxCreator Reference

`../InpxCreator` has `--process=fb2|usr|all`.

- `fb2`: `FileType = 'fb2'`.
- `usr`: `FileType != 'fb2'`.
- `all`: no `FileType` filter.
- Archive matching uses numeric stem first, then `libfilename.FileName`.
- For archive entries absent from DB, it writes dummy INPX placeholders to preserve archive entry order in generated INPX files.
- `metabib` should keep real dataset records for unmatched archive inventory, but INPX generators must still write dummy INPX lines when an archive position has no usable DB/metadata claims.
- `InpxCreator` does not provide binary metadata extraction for EPUB/PDF/DJVU.

## Manifest Scope

Database manifests should become all-books by default. This is required for merge-time DB matching of USR archive records.

Archive manifests should use mutually exclusive scope-specific compatibility:

- `fb2` archive scope keeps existing `metabib.archive_manifest/1` semantics and existing v1 manifests remain valid.
- `usr` archive scope uses new non-FB2 inventory semantics and a new archive manifest schema/scope.
- Mixed FB2/non-FB2 archives are out of first-pass scope. They should either be rejected with a clear warning/error or processed by the existing FB2-only path only when explicitly classified as FB2-only by naming/layout.

The database manifest schema should be bumped because DB scope changes from FB2-only to all books.

The archive manifest schema should be bumped only for `usr` archive inventory. Do not invalidate existing FB2-only archive manifests.

Recommended schema names/scopes:

- `metabib.archive_manifest/1` for `fb2` archive manifests
- `metabib.archive_manifest/2` for `usr` archive manifests
- `metabib.database_manifest/2`

Archive manifest planning should classify archive scope before validation:

- Database: always all-books mode.
- Archive: exactly one scope, either `fb2` or `usr`.
- `fb2` archive: accept v1 manifest if all existing processing settings match.
- `usr` archive: require v2 manifest.
- Mixed archive: out of scope for first pass; log clear diagnostic and do not silently reuse a v1 manifest for non-FB2 records.
- If `fb2` archive processing sees non-FB2 book data, warn clearly and ignore that data.
- If `usr` archive processing sees FB2 book data, warn clearly and ignore that data.

## Database Processing

Change DB book ID scan from FB2-only to all books:

```sql
SELECT BookId FROM libbook ORDER BY BookId
```

Use format-specific ID column as today.

Database records should preserve `DBBook.FileType`. For merge, DB `FileType` can fill `book_extension` when archive-side inference cannot.

Logging changes:

- Rename `Database FB2 book list prepared` to `Database book list prepared`.
- Log counts by `FileType` when cheap enough, or compute during manifest creation.

## Archive Entry Model

Represent archive record identity with both physical container extension and logical book extension.

Proposed internal fields:

```go
type RecordID struct {
    Library            string       `json:"library"`
    BookID             int64        `json:"book_id,omitempty"`
    FileName           string       `json:"file_name,omitempty"`
    Extension          string       `json:"extension,omitempty"`           // logical book extension
    ContainerExtension string       `json:"container_extension,omitempty"` // physical entry container extension
    Archive            *ArchiveInfo `json:"archive,omitempty"`
}
```

Rules:

- Direct `596504.pdf`: `extension=pdf`, `container_extension` empty.
- Nested `592481.djvu.zip`: `extension=djvu`, `container_extension=zip`.
- Unknown nested `Something.zip`: infer `extension` from first non-FBD inner file if inspection succeeds; else leave logical extension empty or `zip` only as media type fallback.
- If merge finds DB match and logical extension is empty, use DB `FileType` as logical extension.
- If outer name says `.pdf.zip` but inner file is `.djvu`, outer name wins and logical extension is `pdf`.

Dataset artifact name for nested archives should be the outer entry name, for example `592481.djvu.zip`. Inner names are not stored.

## Archive Classification

Ignore directories and backup entries before classification. Current backup rule is `.org`; keep that rule. If an ignored backup entry has a matching `.fbd`, ignore both and log debug only.

Classify outer entries:

- `.fbd`: sidecar candidate, not a book entry.
- `.fb2`: FB2 book entry in `fb2` scope; cross-scope data in `usr` scope.
- Known nested archive extension: opaque book artifact plus optional sidecar scan.
- Other file: non-FB2 book entry.

Cross-scope data handling:

- In `fb2` scope, non-FB2 book entries are ignored with `log.Warn` per occurrence.
- In `usr` scope, direct `.fb2` book entries are ignored with `log.Warn` per occurrence.
- `.fbd` sidecars are allowed only in `usr` scope. If found in `fb2` scope, warn and ignore.
- These warnings should include archive path, entry name, entry index, detected extension, selected archive scope, and selected behavior.

Known nested archive extensions should include as many pure-Go formats as feasible:

- `.zip` via stdlib.
- `.rar` via `github.com/nwaples/rardecode`.
- `.7z` via pure-Go 7z reader, likely `github.com/bodgit/sevenzip`.
- `.tar`, `.tar.gz`, `.tgz`, `.tar.bz2`, `.tar.xz`, `.tar.zst` via stdlib plus pure-Go decompressors.

All nested archive entries inside USR archives are potential books. DB lookup uses the nested archive outer name, not inner filenames.

## Nested Archive Inspection

Nested archive inspection exists primarily to find `.fbd` sidecars. It is not required for DB matching.

Rules:

- Treat nested archive as one opaque book artifact.
- Use outer nested archive name for DB lookup and identity candidates.
- Inspect inner directory only when compressed outer entry size is at or below configured cap.
- Default cap: `498 MiB` compressed size.
- Cap should be configurable in YAML.
- If inspection fails, emit opaque outer-entry record and log warning.
- If nested archive is encrypted, unsupported, corrupt, or over cap, emit opaque outer-entry record and log warning.
- If inspection succeeds and one `.fbd` exists, parse it as metadata for whole nested archive.
- If multiple `.fbd` files exist, log warning and ignore all FBD metadata for that nested archive.
- If no `.fbd` exists, ignore inner structure after optionally inferring logical extension from first non-FBD inner file.
- Inner filenames are not stored in records or dataset output.

Nested inspection should avoid temp files when possible. Some archive readers require `io.ReaderAt`; if needed, spool nested entry bytes to a temp file in system temp dir. Warn and emit opaque record on spool/open failure.

## FBD Sidecars

`.fbd` files are FictionBook XML metadata sidecars, not book entries.

Do not count `.fbd` files as dataset archive entries beyond the raw physical archive entry count. In per-kind stats, count them as sidecars, not records.

Direct outer `.fbd` pairing:

- Pair `book.fbd` with sibling `book.ext` case-insensitively.
- Pair by descriptive stem and numeric stem.
- Expected direct pattern is `book.ext` plus `book.fbd`; no need to support direct `book.ext.zip` pairing for outer `.fbd` initially.
- If no corresponding book is found, log warning and ignore `.fbd`.
- If `.fbd` cannot be parsed, log warning and ignore `.fbd`; still emit book record.

Nested `.fbd` pairing:

- Single nested `.fbd` applies to the whole nested archive entry.
- Multiple nested `.fbd` files cause warning and all nested `.fbd` metadata is ignored.
- If both outer direct `.fbd` and nested inner `.fbd` could apply, nested inner `.fbd` wins; outer `.fbd` is ignored with warning.

Use existing FB2 parser in metadata-only mode for `.fbd`:

```go
fb2.ParseWithOptions(reader, fb2.ParseOptions{
    PreserveDescription: cfg.Processing.FB2DescriptionTree,
    BodyFingerprints:    false,
})
```

Reason: `.fbd` may contain large `<binary>` data after `<description>`. Metadata-only parsing stops after description and does not read body/binary payloads.

## Source Model

Do not store `.fbd` metadata in `FB2Source`. `FB2Source` should mean the book entry itself is FB2 content.

Use a neutral sidecar source while reusing `FB2Description` payload:

```go
type RecordSources struct {
    Database DatabaseSource  `json:"database"`
    FB2      FB2Source       `json:"fb2,omitempty"`
    Sidecars []SidecarSource `json:"sidecars,omitempty"`
}

type SidecarSource struct {
    Present     bool            `json:"present"`
    Kind        string          `json:"kind"`   // "fbd"
    Format      string          `json:"format"` // "fictionbook-description"
    Entry       string          `json:"entry"`
    Description *FB2Description `json:"description,omitempty"`
}
```

Dataset conversion:

- Real FB2 book metadata creates observation `fb2` as today.
- FBD sidecar creates observation `fbd`.
- Claims from `.fbd` use `Observation: "fbd"`.
- EPUB/PDF metadata added later should become separate observations, not overwrite FBD.

## INPX Generation

MHL-INPX generation depends on physical archive entry order. It must keep writing dummy lines for positions that cannot produce a real INPX row.

Current `mhlinpx` already writes dummy lines for missing archive indexes using dataset archive `entries` and `ignored` ranges. It also writes a dummy line when a dataset record has neither database nor FB2 metadata. Preserve that behavior for USR records.

Required behavior:

- Keep emitting dataset records for unmatched USR archive entries.
- During MHL-INPX generation, if a USR record has no DB claims and no sidecar/binary metadata claims, write a dummy INPX line at that archive index.
- If a USR record has FBD metadata, MHL-INPX should treat `fbd` as a metadata source equivalent to FB2 fallback for INPX row construction.
- `slice-inpx` filtering can continue to operate on dataset records and does not need dummy rows for missing archive indexes.

Implementation note: `internal/inpxutil.DatasetRecordClaims` currently uses observation `fb2` for `HasFB2` and FB2 fallback fields. It should learn a separate sidecar view, or fold `fbd` claims into the fallback metadata view used by `mhlinpx`, while preserving claim provenance in the dataset.

## Binary Metadata Extraction

Binary metadata extraction is a later layer and should keep separate observations.

Initial extractor interface:

```go
type metadataExtractor interface {
    Match(extension string) bool
    Extract(ctx context.Context, r io.Reader, size uint64) (model.ExtractedMetadataSource, error)
}
```

Potential extractors:

- EPUB: parse `META-INF/container.xml`, then OPF metadata.
- PDF: parse document info and XMP metadata where feasible.
- DJVU: defer until pure-Go metadata option is clear.

Keep both sidecar and binary metadata if both exist.

## Checksums And Fingerprints

Collision fingerprints:

- Keep FB2 body fingerprints only for real `.fb2` book entries.
- Never create collision fingerprints for non-FB2 entries.
- Never create FB2 body fingerprints from `.fbd` sidecars.

Archive content MD5:

- Direct entry: hash entry content bytes.
- Nested archive entry: hash nested archive entry bytes.
- Do not hash inner book bytes for nested archive records.
- Do not hash the whole outer archive file for an individual nested book record.

## Merge-Time DB Matching

Archive manifests remain DB-independent. DB-present counts are reported only during merge. Merge must accept both v1 FB2-only archive manifests and v2 USR/non-FB2 archive manifests in one dataset run.

Matching order:

1. Numeric book ID parsed from archive-side stem.
2. Filename aliases from DB manifest index.

Filename candidates should include:

- `RecordID.FileName`.
- `RecordID.FileName + "." + RecordID.Extension`.
- Archive entry basename.
- Full archive entry path.
- Nested outer entry stem and full name.
- Database filenames from matched DB source when available.

For nested archives, do not use inner filenames as DB match candidates.

If DB match exists and archive-side logical extension is empty, use DB `FileType` as logical extension.

## Logging

Use `log.Debug` for trace-level decisions:

- Archive entry classified.
- Non-FB2 entry selected.
- Nested archive selected as opaque book artifact.
- Nested archive inspection started/completed.
- Logical extension inferred.
- FBD sidecar paired.
- Database numeric match.
- Database filename alias match.

Use `log.Warn` per occurrence for abnormal sidecar/nested behavior:
- Unpaired `.fbd` ignored.
- Multiple `.fbd` sidecars ignored.
- `.fbd` parse failed.
- Nested archive inspection failed.
- Nested archive encrypted/unsupported/corrupt.
- Nested archive over compressed-size cap.
- Outer `.fbd` ignored because nested `.fbd` wins.

Warnings should include as much context as possible:

- outer archive path
- outer entry name
- outer entry index
- nested format
- compressed size
- cap
- sidecar entry name
- candidate book entry name when known
- parse/open error
- selected behavior

## Statistics

Archive cache logs should include per archive:

- total physical entries
- emitted records
- FB2 records
- non-FB2 records
- nested archive records
- sidecars found
- sidecars used
- sidecars ignored
- sidecar parse errors
- nested inspections attempted
- nested inspections succeeded
- nested inspections failed
- nested inspections over cap
- records by logical extension

Merge logs should include per archive and total:

- records
- DB-present records
- DB-absent records
- numeric DB matches
- filename DB matches
- conflicting match evidence
- DB-present/absent by logical extension

DB-present statistics are logs only, not dataset header fields.

## Configuration

Add processing config for nested archive inspection:

```yaml
processing:
  nested_archive_inspection:
    enabled: true
    max_compressed_size_mib: 498
```

Names can be adjusted to match project style. Avoid CLI flags for this feature.

## Implementation Plan

1. Bump database manifest schema to all-books.
2. Add archive manifest v2 for `usr` inventory while preserving v1 `fb2` manifests.
3. Add archive scope classification before manifest validation.
4. Change `usr` archive entry listing from FB2-only to non-FB2 inventory.
5. Extend internal record identity for `container_extension` and logical book extension.
6. Preserve current unmatched-FB2 behavior for unmatched non-FB2 records.
7. Add direct `.fbd` sidecar collection and pairing.
8. Add `SidecarSource` model and dataset conversion to `fbd` observation/claims.
9. Add nested archive detector and pure-Go archive reader abstraction.
10. Implement nested `.zip`, `.rar`, `.7z`, and tar-family inspection.
11. Add nested compressed-size cap config.
12. Add merge-time DB match statistics.
13. Add detailed debug/warn logs.
14. Add tests for v1 FB2 manifest reuse, direct non-FB2 entries, unmatched non-FB2, direct `.fbd`, nested `.fbd`, multiple `.fbd`, parse-failed `.fbd`, over-cap nested archive, and DB merge stats.
15. Validate against a sample from `/mnt/grumpy/library/flibusta_usr`.

## Open Decisions

- Confirm DB manifest all-books default. This design assumes yes.
- Decide JSON/schema naming for logical extension vs container extension in dataset artifacts.
- Decide exact pure-Go 7z dependency after license/API check.
