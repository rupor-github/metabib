# Non-FB2 Fetch And Rollup Plan

## Goal

Extend `fetch` and `rollup` so current Flibusta and Librusec daily non-FB2
updates can be downloaded and rolled into local `usr-*` archives alongside
existing `fb2-*` archives.

This document records the design implemented for non-FB2 daily updates.

## Current State

- `fetch --library NAME` selects one profile from `fetch.libraries`.
- Default profiles are `flibusta` and `librusec`, both FB2-oriented.
- `fetch` finds new daily archive links by range end and downloads SQL dumps
  unless `--nosql` is set.
- `fetch` high-water detection understands finalized `fb2-START-END.zip`, active
  `fb2-START-END.merging`, and retained daily update names.
- `rollup` reads daily update ZIPs, copies non-empty numeric entries without
  recompression, finalizes size-bounded `fb2-START-END.zip` archives, and keeps
  active `fb2-START-END.merging`.
- Archive cache processing already has `fb2` and `usr` scope support. USR scope
  keeps non-FB2 records, pairs `.fbd` sidecars, inspects nested archives when
  enabled, and ignores FB2 entries.

## Observed Remote Names

Flibusta current daily index exposes paired FB2 and non-FB2 updates:

```text
f.fb2.886760-886842.zip
f.n.886760-886842.zip
```

Librusec current daily index exposes dated/rand-suffixed updates with content
extension before `.zip`:

```text
2026-07-12.818211-818248.503.fb2.zip
2026-07-12.818211-818248.503.pdf.zip
2026-07-13.818249-818316.469.djvu.zip
2026-07-18.818528-818590.41.doc.zip
2026-07-26.819012-819063.670.epub.zip
```

## Decisions

- Add selectable fetch profiles: `flibusta-all`, `flibusta-usr`,
  `librusec-all`, `librusec-usr`.
- Keep existing `flibusta` and `librusec` profiles as FB2-only defaults.
- `*-all` means FB2 plus non-FB2 daily update archives.
- `*-usr` means non-FB2 daily update archives only.
- New profiles still download SQL dumps by default, same as existing profiles;
  `--nosql` remains explicit archive-only mode.
- Rollup should not require a content flag. It should detect update families and
  advance `fb2-*` and `usr-*` archive lineages independently in one run.
- Non-FB2 local rollup names should be `usr-START-END.zip` and
  `usr-START-END.merging`.
- Fetch profile regexes should select exactly the archives intended by that
  profile. In particular, `librusec-usr` should not match `.fb2.zip` and then
  rely on code to discard it.
- Fetch profile regexes should be processed with `github.com/dlclark/regexp2`,
  so `librusec-usr` can use negative lookahead and select any non-FB2 daily
  extension without hard-coded extension lists.
- Current-range missing-file recovery is enough for interrupted `*-all` fetches;
  no explicit backfill mode is planned now.
- Per-family rollup counts in logs are enough; no machine-readable CLI output is
  planned now.

## Proposed Fetch Design

### Configuration

Add profile content metadata so high-water selection is explicit. Compile fetch
profile `archive_pattern` and `sql_pattern` with `github.com/dlclark/regexp2`
instead of Go stdlib `regexp`, so profile selection can use lookarounds and keep
selection logic visible in the regex itself:

```yaml
fetch:
  libraries:
    - name: flibusta
      library_name: flibusta
      archive_content: fb2
      archive_pattern: '(?i)<a\s+href="(f(?:\.fb2)?\.[0-9]+-[0-9]+\.zip)">'

    - name: flibusta-usr
      library_name: flibusta
      archive_content: usr
      archive_pattern: '(?i)<a\s+href="(f\.(?!fb2\.)[^.]+\.[0-9]+-[0-9]+\.zip)">'

    - name: flibusta-all
      library_name: flibusta
      archive_content: all
      archive_pattern: '(?i)<a\s+href="(f(?:\.[^.]+)?\.[0-9]+-[0-9]+\.zip)">'

    - name: librusec
      library_name: librusec
      archive_content: fb2
      archive_pattern: '(?i)<a\s+href="([0-9]{4}-[0-9]{2}-[0-9]{2}\.[0-9]+-[0-9]+\.[0-9]+\.fb2\.zip)">'

    - name: librusec-usr
      library_name: librusec
      archive_content: usr
      archive_pattern: '(?i)<a\s+href="([0-9]{4}-[0-9]{2}-[0-9]{2}\.[0-9]+-[0-9]+\.[0-9]+\.(?!fb2\.)[^.]+\.zip)">'

    - name: librusec-all
      library_name: librusec
      archive_content: all
      archive_pattern: '(?i)<a\s+href="([0-9]{4}-[0-9]{2}-[0-9]{2}\.[0-9]+-[0-9]+\.[0-9]+\.[^.]+\.zip)">'
```

`archive_content` values: `fb2`, `usr`, `all`.

Compatibility rule: missing `archive_content` should behave like `fb2` for old
configs, because all shipped profiles were FB2-only.

Configured fetch patterns should use a small `regexp2` match timeout to avoid
pathological regex stalls from user configuration.

### Link Classification

After regex extraction, classify each archive file name:

- FB2 update names:
  `f.START-END.zip`, `f.fb2.START-END.zip`,
  `YYYY-MM-DD.START-END.RAND.fb2.zip`.
- USR update names:
  `f.EXT.START-END.zip`, `YYYY-MM-DD.START-END.RAND.EXT.zip` where
  `EXT != fb2` and extension is currently accepted by config/profile.
- Unknown update names selected by regex should fail with a clear error rather
  than being silently assigned to FB2 or USR.

### High-Water Tracking

Compute separate high-water marks per archive family from `--to`:

- FB2: finalized `fb2-START-END.zip`, active `fb2-START-END.merging`, and FB2
  daily update names.
- USR: finalized `usr-START-END.zip`, active `usr-START-END.merging`, and USR
  daily update names.

Selection rules:

- `archive_content: fb2`: compare every selected link with FB2 high-water.
- `archive_content: usr`: compare every selected link with USR high-water.
- `archive_content: all`: classify each link and compare it with that family's
  high-water.
- Fetch profile regexes should not select files that the profile does not intend
  to download. Content classification still determines which family high-water is
  used for `archive_content: all`, but it should not be used as a hidden exclude
  filter for `archive_content: usr`.
- When a link has same range end as family high-water and the exact destination
  file is missing, download it to recover from interrupted `all` or multi-format
  fetches for the current newest range.
- Do not backfill arbitrary older missing same-range files automatically; users
  can remove local high-water artifacts or use a separate backfill workflow if
  that is needed later.

### CLI

No new fetch flag required. Existing selection remains:

```sh
metabib fetch --library flibusta-all --to upd_flibusta --tosql flibusta_YYYYMMDD --continue
metabib fetch --library flibusta-usr --to upd_flibusta_usr --tosql flibusta_YYYYMMDD --continue
metabib fetch --library librusec-all --to upd_librusec --tosql librusec_YYYYMMDD --continue
metabib fetch --library librusec-usr --to upd_librusec_usr --tosql librusec_YYYYMMDD --continue
```

Exit codes stay unchanged: `2` when any new archive update was downloaded.

## Proposed Rollup Design

### Automatic Family Split

`rollup` should scan `--archives` and all `--updates` directories, classify files
with `rollup.update_patterns` from configuration, then process each family with
existing rollup logic:

- FB2 local finalized archives: `fb2-START-END.zip`.
- FB2 active archive: `fb2-START-END.merging`.
- FB2 updates: filenames matching configured patterns with `family: fb2`.
- USR local finalized archives: `usr-START-END.zip`.
- USR active archive: `usr-START-END.merging`.
- USR updates: filenames matching configured patterns with `family: usr`.

Default patterns are:

```yaml
rollup:
  update_patterns:
    - name: flibusta-fb2
      family: fb2
      pattern: '(?i)^f(?:\.fb2)?\.([0-9]+)-([0-9]+)\.zip$'
    - name: flibusta-usr
      family: usr
      pattern: '(?i)^f\.(?!fb2\.)[^.]+\.([0-9]+)-([0-9]+)\.zip$'
    - name: librusec-fb2
      family: fb2
      pattern: '(?i)^[0-9]{4}-[0-9]{2}-[0-9]{2}\.([0-9]+)-([0-9]+)\.[0-9]+\.fb2\.zip$'
    - name: librusec-usr
      family: usr
      pattern: '(?i)^[0-9]{4}-[0-9]{2}-[0-9]{2}\.([0-9]+)-([0-9]+)\.[0-9]+\.(?!fb2\.)[^.]+\.zip$'
```

Each pattern is matched against local file names with `regexp2`. Capture group 1
must be range begin, and capture group 2 must be range end. If a filename matches
multiple patterns, rollup fails with an ambiguity error instead of using hidden
precedence.

One invocation can update both active archives:

```sh
metabib rollup --archives flibusta --updates upd_flibusta
```

Possible outputs from same run:

```text
fb2-0000886760-0000887123.merging
usr-0000886760-0000887123.merging
```

### Per-Family Invariants

Preserve existing safety rules per family:

- Allow at most one active `.merging` archive per family.
- Validate active `.merging` range against latest finalized archive in same
  family.
- Use name width from same family's latest finalized or active archive; default
  to 10 digits for new lineages.
- Finalize only when that family's active work archive reaches
  `rollup.finalization.size.target_mib.<family>` converted to bytes.
- Return rollup exit code `2` if any family finalizes at least one archive.

Default target sizes:

```yaml
rollup:
  finalization:
    policy: size
    size:
      target_mib:
        fb2: 2048
        usr: 4096
```

### USR Duplicate Handling

FB2 duplicate suppression by numeric book ID is not valid for USR archives,
because one book ID can have multiple non-FB2 formats. USR rollup should treat
entries as duplicates only when their archive entry identity is the same.

Recommended USR identity key:

```text
lowercase(base entry name including extension and container extension)
```

Examples that should coexist in one `usr-*` archive:

```text
818528.pdf
818528.djvu
818528.doc
818528.epub
818528.pdf.zip
```

Existing cache rules will later ignore FB2 content if it appears in a USR archive,
but rollup classification should prevent FB2 daily updates from entering `usr-*`
archives in the first place.

### Ordering

Within each family:

- Sort update archives by begin/end range and then file name.
- Sort entries by numeric book ID and then entry name.
- Preserve compressed entry data with `zip.Writer.Copy` as today.

Sorting by entry name after book ID gives deterministic order for same-ID USR
formats.

## Cache And Merge Impact

No schema change expected for cache or merged datasets.

- `usr-START-END.zip` should classify as USR scope by both archive name and
  archive content when it contains non-FB2 entries.
- `isUSRArchivePath` already recognizes basename prefix `usr-`; tests should lock
  in `usr-START-END.zip` behavior.
- `.merging` remains operational rollup state and should not be selected by
  `cache --archives DIR` or `merge --archives DIR`, because those commands expand
  only `.zip` files today.

Typical pipeline after extension:

```sh
metabib fetch --library flibusta-all --to upd_flibusta --tosql flibusta_YYYYMMDD --continue
metabib rollup --archives flibusta_archives --updates upd_flibusta
metabib cache --database-dumps flibusta_YYYYMMDD --archives flibusta_archives
metabib merge --database-dumps flibusta_YYYYMMDD --archives flibusta_archives --output flibusta
```

## Implementation Tasks

Implemented tasks:

1. Add `archive_content` to fetch profile config with validation/defaulting.
2. Add default `flibusta-all`, `flibusta-usr`, `librusec-all`, and
   `librusec-usr` profiles.
3. Replace single fetch high-water with per-family high-water detection.
4. Add configurable `rollup.update_patterns` with `regexp2` matching and
   ambiguity detection. Fetch profile regexes select intended files; rollup
   patterns classify selected local update files into FB2 or USR lineages.
5. Parameterize rollup internals by family prefix and duplicate key policy.
6. Make `rollup.Run` process FB2 and USR families independently in one call.
7. Extend result/log output to report per-family updates, finalizations, active
   merge archive, and finalized archive paths.
8. Update README usage and config docs after code behavior is finalized.

## Test Plan

- Fetch config loads old profiles without `archive_content` as FB2.
- New default profiles match observed Flibusta and Librusec names.
- Fetch high-water detects `fb2-*` and `usr-*` finalized and active archives
  independently.
- `flibusta-all` downloads `f.fb2.START-END.zip` plus any selected
  `f.EXT.START-END.zip` non-FB2 update when both are newer.
- `librusec-all` downloads FB2 plus every selected non-FB2 extension archive.
- Interrupted same-range multi-format fetch can download missing current-range
  files on next run.
- Rollup with mixed FB2 and USR updates creates or updates both `fb2-*.merging`
  and `usr-*.merging`.
- Rollup rejects multiple `fb2-*.merging` files and multiple `usr-*.merging`
  files, but allows one of each.
- Rollup keeps same-ID USR entries with different extensions.
- Rollup still suppresses true duplicate FB2 entries by numeric book ID.
- Cache classifies `usr-START-END.zip` as USR and ignores `.merging` files when
  scanning directories.

## Resolved Questions

- Fetch profile regexes should make selection logic visible and avoid selecting
  files that code later discards.
- Fetch profile regexes use `regexp2`, so default Librusec non-FB2 regex uses
  `(?!fb2\.)[^.]+` to select any extension except `fb2`.
- Current-range missing-file recovery is enough; no `fetch --backfill-missing`
  mode now.
- Logs are enough for per-family rollup counts.
