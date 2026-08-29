# Rollup Period Finalization Design

## Goal

Extend `metabib rollup` with optional time-based archive finalization while
keeping existing size-budget behavior unchanged.

Today `rollup` folds daily FB2 and USR update ZIPs into active
`fb2-START-END.merging` / `usr-START-END.merging` archives and finalizes them as
`fb2-START-END.zip` / `usr-START-END.zip` when the configured compressed-size
budget is exhausted. New period policies should allow accumulated books to be
finalized by elapsed runtime or UTC calendar bucket instead of size.

## Non-Goals

- Do not change existing `size` behavior.
- Do not infer historical period state from existing `.merging` files.
- Do not use update filename dates, archive entry metadata, database metadata, or
  local timezone for period decisions.
- Do not change finalized archive filename format unless a later design requires
  it.
- Do not configure separate finalization policies per archive lineage. `fb2` and
  `usr` always follow the same rollup finalization policy.
- Do not apply size limits to `rolling` or `calendar` policies. Period policies
  intentionally ignore archive size.

## Policies

### Size

`size` remains the default policy and preserves current behavior. It does not
require or update period state.

### Rolling

`rolling` finalizes an active `.merging` archive after a configured UTC duration
from the time that active merge first started accumulating entries.

Example configuration:

```yaml
rollup:
  finalization:
    policy: rolling
    rolling:
      duration: 14d
```

Semantics:

- `started_at` is set when the first valid new entry is copied into a fresh active
  merge lineage.
- `deadline` is `started_at + rolling`.
- A later run finalizes the active merge when `now >= deadline`.
- Durations are stored as Go `time.Duration` values internally. Configuration
  accepts whole-day (`d`) and whole-week (`w`) units only; hours, minutes,
  seconds, and sub-second units are intentionally rejected for rollup
  finalization policy.

### Calendar

`calendar` finalizes an active `.merging` archive when the current UTC time has
left the stored calendar bucket.

Example configuration:

```yaml
rollup:
  finalization:
    policy: calendar
    calendar:
      bucket: month
```

Supported UTC buckets:

- `iso-week`: Monday `00:00:00` UTC through next Monday `00:00:00` UTC.
- `iso-biweek`: ISO weeks `1-2`, `3-4`, etc.; ISO week `53` is a single-week
  bucket.
- `month`: first day of month `00:00:00` UTC through first day of next month
  `00:00:00` UTC.

Semantics:

- Bucket is computed from UTC `now` when the active merge first starts
  accumulating entries.
- `bucket_start` and `bucket_end` are stored explicitly.
- A later run finalizes the active merge when `now >= bucket_end`.

## Configuration Shape

Proposed configuration:

```yaml
rollup:
  finalization:
    policy: size # size | rolling | calendar

    size:
      target_mib:
        fb2: 2048
        usr: 4096

    rolling:
      duration: 14d

    calendar:
      bucket: month # iso-week | iso-biweek | month
```

Validation rules:

- Missing `finalization` behaves like `policy: size`.
- `size.target_mib.fb2` and `size.target_mib.usr` are required only when
  `policy: size`.
- `rolling.duration` is required only when `policy: rolling`.
- `calendar.bucket` is required only when `policy: calendar`.
- The same configured policy applies to both `fb2` and `usr` lineages.
- Time calculations use UTC only.
- Period policies ignore `size.target_mib`; that setting is used only by
  `policy: size`.

## State File

Period policies need explicit state because current `.merging` names only encode
book ID range. File mtime is not reliable: rollup rewrites merge archives through
temporary files and atomic rename, so mtime reflects last rewrite rather than
start time.

Store state in the archive directory, for example `rollup-state.json`.

Calendar example:

```json
{
  "version": 1,
  "lineages": {
    "fb2": {
      "active_merge": "fb2-0000000001-0000000100.merging",
      "first_book": 1,
      "last_book": 100,
      "policy": "calendar",
      "calendar": "iso-biweek",
      "bucket_start": "2026-08-24T00:00:00Z",
      "bucket_end": "2026-09-07T00:00:00Z"
    }
  }
}
```

Rolling example:

```json
{
  "version": 1,
  "lineages": {
    "usr": {
      "active_merge": "usr-0000000001-0000000100.merging",
      "first_book": 1,
      "last_book": 100,
      "policy": "rolling",
      "rolling": "14d",
      "started_at": "2026-08-29T12:00:00Z",
      "deadline": "2026-09-12T12:00:00Z"
    }
  }
}
```

State write rules:

- Write state atomically after publishing a `.merging` archive.
- Remove lineage state after finalizing that lineage's `.merging` archive to
  `.zip`.
- If a run finalizes an old `.merging` and then starts a new `.merging`, write new
  state for the new active merge.

## Existing Merge Handling

When `policy: size`:

- Existing behavior remains unchanged.
- State file is ignored by size-budget finalization.

When `policy: rolling` or `policy: calendar`:

- If no `.merging` exists for a lineage, rollup starts clean and creates state
  when first valid new entry is copied.
- If `.merging` exists and matching state exists for the lineage, rollup uses
  stored state.
- If `.merging` exists and state is missing, invalid, or missing the required
  lineage, rollup fails with a clear error.
- Rollup does not silently migrate, repair, or guess period start time.

State must match detected merge archive:

- `active_merge` basename must match `.merging` filename.
- `first_book` and `last_book` must match the range encoded in `.merging`.
- Stored `policy` and policy-specific fields must match current configuration.

## Finalization Flow

At start of each lineage run under period policy:

1. Detect last finalized archive and active `.merging` using current range-based
   filename rules.
2. If active `.merging` exists, load and validate lineage state.
3. If stored deadline or bucket end has passed, finalize `.merging` immediately.
4. Continue processing available updates.
5. If copied entries remain below period boundary, publish `.merging` and update
   state.

Period policies finalize only at run boundaries or before processing new entries.
They do not split a single run by elapsed wall-clock time while copying entries.

Finalization logs should include the reason:

- `size` for size-budget finalization.
- `rolling_deadline` for rolling-duration deadline expiry.
- `calendar_bucket_end` for UTC calendar bucket expiry.

## Compatibility

Keep archive names compatible with existing fetch high-water detection and later
manifest/INPX processing:

- Finalized: `fb2-START-END.zip`, `usr-START-END.zip`.
- Active: `fb2-START-END.merging`, `usr-START-END.merging`.

State is additional sidecar data and does not change archive consumers.

## Implementation Notes

- Move size thresholds from old `rollup.target_size_mib` to
  `rollup.finalization.size.target_mib`. No backwards-compatible alias is needed
  because the previous config shape was not released.
- Rolling duration parsing accepts whole-day (`d`) and whole-week (`w`) units and
  converts them to Go `time.Duration` values.
- Finalization reason should be present in structured logs for all policies.
