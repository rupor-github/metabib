# Downloads and archive rollup

[User guide](guide.md) · [Cache manifests](cache.md)

Use `fetch` to download daily archive updates and SQL dumps, then `rollup` to
combine daily ZIPs into local archives. FB2 and USR/non-FB2 updates are tracked
independently.

## Contents

- [Download updates](#download-updates)
- [Fetch options](#fetch-options)
- [Resume and troubleshoot downloads](#resume-and-troubleshoot-downloads)
- [Roll up daily archives](#roll-up-daily-archives)
- [Choose a finalization policy](#choose-a-finalization-policy)
- [Exit codes for automation](#exit-codes-for-automation)

## Download updates

```sh
metabib fetch --library flibusta --to upd_flibusta --tosql flibusta_sql --continue
```

Keep the same output directories on retries so partial files can be reused.
`fetch` downloads only daily archive updates newer than the local high-water
mark, and decompresses `*.sql.gz` dumps into `--tosql`.

### Choose a profile

Profiles live under `fetch.libraries` in the YAML configuration.
They select download sources, not the dataset's
[library name](guide.md#library-name).

| Profile | Daily archive content | SQL dumps |
| --- | --- | --- |
| `flibusta` | FB2 | Yes |
| `flibusta-usr` | Non-FB2 | Yes |
| `flibusta-all` | FB2 and non-FB2 | Yes |
| `librusec` | FB2 | Yes |
| `librusec-usr` | Non-FB2 | Yes |
| `librusec-all` | FB2 and non-FB2 | Yes |

For example:

```sh
metabib fetch --library flibusta-all --to upd_flibusta --tosql flibusta_sql --continue
metabib fetch --library librusec --to upd_librusec --tosql librusec_sql --continue
metabib fetch --library librusec-usr --to upd_librusec_usr --tosql librusec_sql --continue
```

To download SQL only:

```sh
metabib fetch --library flibusta --noarchives --tosql flibusta_sql
```

USR selection uses `regexp2` negative lookahead to accept daily extensions other
than `fb2`, without an extension allowlist. Flibusta SQL profiles select
`lib.lib*.sql.gz` and `lib.b.annotations.sql.gz`, not
`lib.b.annotations_pics.sql.gz` or `lib.a.*`.

### How "newer" is determined

`fetch` examines range-named archives, active `.merging` files, and retained daily
updates in `--to`. Each family has its own highest known book ID.
A newer FB2 file never suppresses a USR download, or vice versa.
Combined profiles classify each remote update before comparing it with the
corresponding family's high-water mark.

## Fetch options

| Option | Meaning |
| --- | --- |
| `--library NAME`, `-l NAME` | Profile name; default `flibusta`. |
| `--to DIR`, `-o DIR` | Daily archive destination; required unless `--noarchives`. |
| `--tosql DIR` | Decompressed SQL destination; defaults to a name based on the library and current UTC timestamp. |
| `--nosql` | Download archive updates only. |
| `--noarchives` | Download SQL only; cannot be combined with `--nosql`. |
| `--retry N` | Attempts per index or file; default `3`. |
| `--timeout SECONDS` | Connection/header and body-read inactivity timeout; default `20`. |
| `--chunksize SIZE` | Verbose progress interval; default `10MiB`. |
| `--continue` | Preserve and safely resume partial downloads. |
| `--sticky` | Ignore redirects and keep using the original host. |

The timeout is **not a total download deadline**: a transfer can continue as long
as it keeps receiving bytes. Transient retries use exponential backoff with jitter
up to 30 seconds; `Retry-After` delays are honored up to 5 minutes. Cancellation
interrupts waits.

`--chunksize` accepts positive whole numbers with `B`, `KiB`, `MiB`, or `GiB`
units, case-insensitively. Bare numbers mean MiB: `1` equals `1MiB`.
Zero, negative, fractional, and overflowing values are rejected.
It controls logging, not HTTP request size or a chunk-completion deadline.

## Resume and troubleshoot downloads

For slow or intermittently stalled connections:

```sh
metabib --verbose fetch --library flibusta --continue --retry 10 --timeout 300 \
  --chunksize 512KiB --to upd_flibusta --tosql flibusta_sql
```

ZIP entries and gzip checksums are validated before output is published
atomically. Invalid or interrupted downloads do not replace existing output.
HTTP, HTTPS, redirects, and profile proxies are supported.

### Partial files

With `--continue`, partial data is stored beside the destination:

- `.<filename>.part`: downloaded bytes;
- `.<filename>.part.meta.json`: source and resume metadata.

Range requests use a strong ETag or Last-Modified validator with `If-Range`.
Without a usable validator, downloads restart rather than mix file versions.
Partial files are removed after successful publication.

### When the server ignores ranges

A proper resume response is `206 Partial Content`, with `Content-Range`
starting at the requested offset.

- `200 OK` with an unchanged validator means the server ignored the range.
  Fetch safely replaces the partial data with the full response, warns, and
  remembers to restart that download on subsequent retries and runs.
- HTTP `400` on a range request triggers an immediate plain GET in the same
  attempt; future retries and runs also restart without ranges.
- No `Accept-Ranges` header alone does **not** prove that resume is unsupported.

Preserving a partial file cannot avoid retransmission if the endpoint does not
support ranges. Changing the source URL starts a fresh download because partial
metadata is tied to the original URL.

Logs include the final URL/status, bytes, attempt elapsed time, inactivity errors,
ETag, Last-Modified, Accept-Ranges, and transfer encoding. When comparing with a
browser, compare its final URL with the logged `final_url`.

## Roll up daily archives

```sh
metabib rollup --archives /path/to/books --updates /path/to/updates
```

| Option | Meaning |
| --- | --- |
| `--archives DIR`, `-a DIR` | Required directory for finalized ZIPs and active `.merging` archives. |
| `--updates DIR`, `-u DIR` | Daily update directory; repeatable. Defaults to `--archives`. |

Rollup copies compressed ZIP entries without recompressing them.
Daily update ZIPs are **preserved**; cleanup is a separate operational task.

Output uses local range names, even for dated Librusec updates:

```text
fb2-0000817672-0000818248.zip
usr-0000817672-0000818248.zip
```

Active archives use `.merging` instead of `.zip`. The ID width comes from the
existing active or latest finalized archive in that family; new directories use
10-digit IDs.

One run can process mixed FB2/USR updates. Each family has its own latest
finalized archive, active archive, and overlap checks, but both use the same
finalization policy.

### Update patterns and CRC validation

`rollup.update_patterns` contains `regexp2` filename patterns. The first two
capture groups must be the range begin and end; `family` selects `fb2` or `usr`.
A file matching multiple patterns is an error, not an implicit precedence choice.

Direct compressed copying does not validate payload CRCs by default.
Set `rollup.validate_crc: true` to decompress each non-empty numeric entry for
CRC-32 validation before copying. This can be much slower, but the copied data
still retains its original compression.

## Choose a finalization policy

Configure one policy under `rollup.finalization`; it applies to both families.
Finalization is checked when `rollup` runs, not by a background timer.

### Size (default)

Finalize when an active archive reaches its compressed-size target in MiB:

```yaml
rollup:
  finalization:
    policy: size
    size:
      target_mib:
        fb2: 2048
        usr: 4096
```

### Rolling period

Finalize after a duration from when the active merge first started accumulating
entries:

```yaml
rollup:
  finalization:
    policy: rolling
    rolling:
      duration: 14d
```

Only whole-day (`d`) and whole-week (`w`) units are accepted; `24h` is rejected.
Period policies ignore size targets.

### UTC calendar period

Finalize when the current UTC time leaves the stored calendar bucket:

```yaml
rollup:
  finalization:
    policy: calendar
    calendar:
      bucket: month
```

Buckets are `iso-week`, `iso-biweek`, or `month`. ISO weeks begin Monday at
00:00 UTC. Biweeks pair weeks 1–2, 3–4, and so on; week 53 stands alone.

### State and policy changes

Period policies store timing in `rollup-state.json` under `--archives`.
Keep that file together with the active archives.

- All active lineages must use the same stored policy.
- A `.merging` archive without matching period state causes an error; rollup
  will not guess when it started.
- With no active archive, a period policy starts cleanly and writes state after
  publishing the first active merge.
- The size policy does not need this state file.

State has a top-level `version: 1` and `lineages` map keyed by `fb2` or `usr`.
Each entry contains `active_merge`, `first_book`, `last_book`, `policy`, and the
selected policy's timing fields. The archive name and book range must match the
`.merging` filename. Finalizing removes that lineage's entry; starting a new
merge writes its entry atomically.

<details>
<summary>Example calendar state with two active families</summary>

```json
{
  "version": 1,
  "lineages": {
    "fb2": {
      "active_merge": "fb2-0000000001-0000000100.merging",
      "first_book": 1,
      "last_book": 100,
      "policy": "calendar",
      "calendar": "month",
      "bucket_start": "2026-08-01T00:00:00Z",
      "bucket_end": "2026-09-01T00:00:00Z"
    },
    "usr": {
      "active_merge": "usr-0000000001-0000000100.merging",
      "first_book": 1,
      "last_book": 100,
      "policy": "calendar",
      "calendar": "month",
      "bucket_start": "2026-08-01T00:00:00Z",
      "bucket_end": "2026-09-01T00:00:00Z"
    }
  }
}
```

</details>

Finalization logs include `policy` and a `reason`: `size`, `rolling_deadline`,
or `calendar_bucket_end`.

## Exit codes for automation

| Code | `fetch` | `rollup` |
| --- | --- | --- |
| `0` | No new archive updates downloaded; also SQL-only success. | No finalized archive produced. |
| `1` | Error. | Error. |
| `2` | New archive updates downloaded. | One or more finalized FB2/USR archives created. |

Code `2` is a successful result you can use to trigger the next stage.
A successful rollup may leave only an active `.merging` archive and return `0`.
