# Code Review Remediation TODO

Generated from the thorough code review on 2026-06-22.

## Workflow

- Address one item at a time.
- Before starting an item, ask the user which item to work on and wait for approval.
- After implementing and validating an item, ask the user to review the diff before committing.
- Commit each completed item separately after explicit approval.
- Do not amend commits unless explicitly requested.
- For Go edits, follow `.opencode/go-best-practices.md` validation: `goimports-reviser`, `gopls check`, and the narrowest relevant tests/checks.

## TODO

- [x] Critical: Prevent managed MariaDB from being exposed without authentication.
  - References: `db/runtime.go:249-264`, `db/runtime.go:99`, `config/cfg.go:29-39`.
  - Notes: Managed server uses `--skip-grant-tables`; TCP host is configurable.

- [x] Critical: Add safety guards around destructive managed data directory overwrite.
  - References: `db/runtime.go:200-205`.
  - Notes: `--db-overwrite` currently calls `os.RemoveAll` on configured `data_dir` without path safety checks.

- [x] Critical: Fix archive batch result indexing so skipped ZIP entries cannot drop batches.
  - References: `library/process.go:411`, `library/process.go:424-499`.
  - Notes: Batches are based on filtered entries but result index is derived from original ZIP index.

- [x] Critical: Make JSONL split filenames unique when ranges repeat or BookID is zero.
  - References: `jsonl/writer.go:88-97`, `jsonl/writer.go:151-169`.
  - Notes: Multiple split parts can resolve to the same final filename and overwrite previous output.

- [x] High: Return final JSONL close, flush, compressor, and rename errors from CLI output paths.
  - References: `cmd/metabib/main.go:606-612`, `jsonl/writer.go:101-105`, `jsonl/writer.go:132-170`.
  - Notes: `writeOutput` defers `out.Close()` and ignores close-time failures.

- [x] High: Ensure rollup removes or replaces superseded finalized archives when merging the last archive.
  - References: `rollup/rollup.go:285-293`, `rollup/rollup.go:357-360`.
  - Notes: Old finalized archives can remain beside the new merged archive and later duplicate books.

- [x] High: Anchor rollup filename regexes and avoid selecting/removing temp or backup files.
  - References: `rollup/rollup.go:422-426`, `rollup/rollup.go:489-490`, `rollup/rollup.go:247-255`.
  - Notes: Substring matches like `fb2.000001-000002.zip.tmp` can be selected as updates.

- [x] High: Handle overlapping rollup updates and compute archive ranges from min/max IDs.
  - References: `rollup/rollup.go:174-193`, `rollup/rollup.go:475-486`.
  - Notes: Overlapping updates are skipped and range names depend on ZIP entry order.

- [x] High: Make archive manifest identity and INPX archive matching robust across relocated paths.
  - References: `library/process.go:583-584`, `library/manifest.go:861-865`, `library/metadata.go:42-49`, `inpx/inpx.go:283-288`, `inpx/inpx.go:358-363`.
  - Notes: Manifest validation compares only basenames, but records and metadata use exact paths.

- [x] High: Avoid central archive manifest collisions for archives with the same basename.
  - References: `library/manifest.go:757-763`, `library/manifest.go:861-865`.
  - Notes: `archive_dir` maps `/a/books.zip` and `/b/books.zip` to the same manifest path.

- [x] High: Prevent `--db-overwrite` from dropping external or DSN databases unintentionally.
  - References: `cmd/metabib/main.go:371-379`, `db/runtime.go:50-60`, `db/importer.go:134-135`.
  - Notes: External DB runs still pass overwrite into importer and execute `DROP DATABASE`.

- [ ] Skipped: Stop exposing DB passwords in process arguments.
  - References: `db/importer.go:277-278`, `db/importer.go:291-292`, `db/runtime.go:110-111`.
  - Notes: Passwords are passed as `--password=<secret>` to MariaDB clients.

- [x] Medium: Validate manifest schema and declared record count during manifest iteration/copy.
  - References: `library/manifest.go:301-335`.
  - Notes: Iteration ignores header schema and does not compare actual records with declared `records`.

- [x] Medium: Enable archive DB fallback lookup for non-numeric FB2 filenames.
  - References: `library/process.go:363-367`, `library/process.go:593-595`.
  - Notes: Non-numeric `.fb2` entries remain `BookID=0` and never look up DB metadata by filename.

- [x] Medium: Preserve FB2 mixed text order and remove cover image metadata.
  - References: `fb2/parse.go:87-91`, `fb2/parse.go:148-150`, `fb2/parse.go:205-215`.
  - Notes: `coverpage` is skipped and mixed text is reordered by `collectText`.

- [ ] Medium: Escape or remove INPX field separators and lone carriage returns.
  - References: `inpx/inpx.go:469-488`, `inpx/inpx.go:656-660`.
  - Notes: Text containing `\x04` or lone `\r` can corrupt `.inp` field layout.

- [ ] Medium: Make repository contributor and filename identity ordering deterministic.
  - References: `db/repository.go:114-128`, `db/repository.go:261-265`, `db/repository.go:494-499`.
  - Notes: Missing tie-breakers can make output unstable across query plans.

- [ ] Medium: Avoid deleting non-socket files during managed Unix socket cleanup.
  - References: `db/runtime.go:257-259`.
  - Notes: Configured `database.socket` is removed without verifying it is a socket.

## Baseline Validation

- `GOEXPERIMENT=jsonv2 go test -mod=mod ./...` passed before remediation.
- `GOEXPERIMENT=jsonv2 GOFLAGS=-mod=mod go tool staticcheck -f stylish -tests=true ./...` passed before remediation.
