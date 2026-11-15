# Automation and repair scripts

[User guide](guide.md)

These scripts are optional helpers, not prerequisites for `metabib`.
Review paths and behavior before using them on your library.

## Contents

- [Flibusta script](#flibusta-script)
- [Download missing Flibusta books](#download-missing-flibusta-books)
- [Replace an FB2 archive range](#replace-an-fb2-archive-range)

## Flibusta script

[`scripts/fb2_flibusta.sh`](../scripts/fb2_flibusta.sh) automates the common
Flibusta **FB2** workflow. Adapt it for your scheduling, paths, cleanup policy,
and catalog choices.

The `metabib` executable must be next to the script. If `metabib.yaml` exists
there, the script passes it to every invocation.

### Run a full update

```sh
scripts/fb2_flibusta.sh /path/to/library full mhl
scripts/fb2_flibusta.sh /path/to/library full flib
scripts/fb2_flibusta.sh /path/to/library full both
```

`full` runs fetch, rollup, cache, merge, and the selected exporter.
After a successful fetch it always checks rollup, including updates from an
earlier failed fetch. It exits early if rollup does not finalize a new archive.

### Reindex existing data

```sh
scripts/fb2_flibusta.sh /path/to/library reindex both
```

`reindex` skips downloads and rollup. It runs cache, merge, and export using the
local archives and latest SQL directory matching `<library-root>/flibusta_*`.

Both modes accept an optional account whose home becomes the working directory,
useful for Synology Task Scheduler:

```sh
scripts/fb2_flibusta.sh /path/to/library full both myuser
```

### Files and outputs

| Location under the library root | Contents |
| --- | --- |
| `flibusta/` | Finalized FB2 archives and the active `.merging` archive. |
| `upd_flibusta/` | Downloaded daily updates. |
| `flibusta_<timestamp>/` | SQL dumps, partial downloads, and an unfinished-fetch marker when needed. |
| `inpx/` | Merged datasets and generated catalogs. |

| Export mode | Output |
| --- | --- |
| `mhl` | `inpx/flibusta_mhl_<dump-date>.inpx` |
| `flib` | `inpx/flibusta_flib_<dump-date>.inpx`, with `--source-lib flibusta --additional` |
| `both` | Both catalogs from the same merged dataset. |

FLibrary additional outputs share the INPX prefix:
`flibusta_flib_<dump-date>-annotations.zip` when annotation data is available,
and `flibusta_flib_<dump-date>-compilations.zip` when fingerprints detect
compilations.

The script maintains stable links under `inpx/flib-etc/`:
`annotations.zip` and, when generated, `compilations.zip`. Stale symlinks are
removed when the corresponding optional artifact is absent.

### Retry failures

For a failed fetch, rerun the same `full` command on the same day:

```sh
scripts/fb2_flibusta.sh /path/to/library full both
```

The script marks unfinished SQL directories with `.fetch-incomplete`.
It reuses the latest marked directory from the current local day, allowing
`fetch --continue` to resume downloads. The marker is removed only after fetch
succeeds.

For example, a 10:30 retry can reuse `flibusta_20261007_080033/` while creating
a new log. If no unfinished directory exists for today, it creates a new
timestamped SQL directory. Retry later indexing failures with `reindex`.

### Cleanup policy

After a successful download, full mode keeps:

- the five newest SQL dump directories;
- the newest SQL directory already containing `database.manifest.zst`, even if
  it is older than those five;
- the current SQL directory, including an older one reused by a same-day retry.

Update cleanup retains up to ten completed ZIPs and leaves partial downloads
and their metadata untouched.

### Logs

Console logs are written next to the script with names such as
`flibusta_full_mhl_20260622_103000.log` or
`flibusta_reindex_both_20260622_103000.log`.

To combine script output, metabib debug messages, and MariaDB process/client
output in that log, use this in the same-directory `metabib.yaml`:

```yaml
logging:
  console:
    level: debug
  file:
    level: none
```

The script otherwise leaves application file logging to metabib's configuration.

## Download missing Flibusta books

[`scripts/download_flibusta_fb2.py`](../scripts/download_flibusta_fb2.py) selects
FB2 records from a database manifest and downloads them sequentially from
`/b/<book_id>/fb2`.

**Requirements:** Python 3, `zstd`, and `curl`. Run from the repository root:

```sh
python3 scripts/download_flibusta_fb2.py \
  /path/to/database.manifest.zst \
  --first 893518 --last 893588 \
  --output build/books
```

| Option | Behavior |
| --- | --- |
| `--first`, `--last` | Both required; inclusive range with `1 <= first <= last`. |
| `--dry-run` | List selected records without downloading. |
| `--base-url` | Default `https://flibusta.site`; change to your mirror. |
| `--delay` | Seconds between books; default `2`. |
| `--output` | Download directory; reuse it to resume. |

Without `--output`, the script creates and prints a retained temporary directory
named `/tmp/missing-books-<random>`.

ZIP responses are unpacked and FB2 XML is validated before publishing each
`<book_id>.fb2`. Valid existing files are skipped on reruns.
Selected manifest records are saved as `selected-records.jsonl`; failed
downloads cause a nonzero exit status.

## Replace an FB2 archive range

[`scripts/repack_fb2.go`](../scripts/repack_fb2.go) replaces an **entire inclusive
book-ID range** in a ZIP with downloaded files. It preserves numeric order and
copies compressed data outside the range unchanged. Only replacement books
are compressed.

This helper needs Go; the prebuilt `metabib` application itself does not.

```sh
go run -mod=mod scripts/repack_fb2.go \
  --input /path/to/fb2-0000892299-0000894172.zip \
  --books build/books \
  --first 893518 --last 893588 \
  --output /path/to/fb2-0000892299-0000894172.repaired.zip \
  --dry-run
```

Check the counts before removing `--dry-run`.

**All original entries in the range are removed**, including IDs with no
replacement file. Downloaded IDs absent from the original ZIP are inserted in
numeric order.

Requirements and safeguards:

- Input must contain flat `<book_id>.fb2` entries in strictly increasing ID order.
- Every replacement must fall within the requested range.
- The input ZIP is never modified; the output must not already exist.
- The output is published only when complete.
- Allow disk space for another full archive, including copied data and rebuilt
  ZIP headers/offsets.

Verify the result before replacing the original:

```sh
unzip -t /path/to/fb2-0000892299-0000894172.repaired.zip
```
