#!/usr/bin/env python3
"""Recover an inclusive FB2 ID range from a metabib database manifest.

Requires Python 3, zstd, and curl. Downloads are strictly sequential.
The output directory is retained, including after failures or interruption.
"""

import argparse
import json
import math
import shutil
import subprocess
import sys
import tempfile
import time
import xml.etree.ElementTree as ET
import zipfile
from pathlib import Path
from urllib.parse import urlsplit


def select_books(manifest, first, last):
    """Stream the manifest; keep only matching database records in memory."""
    books = {}
    with subprocess.Popen(
        ["zstd", "-dc", "--", str(manifest)],
        stdout=subprocess.PIPE,
        encoding="utf-8",
    ) as process:
        try:
            header = json.loads(process.stdout.readline())
            if header.get("schema") != "metabib.database_manifest/2":
                raise ValueError("expected a metabib.database_manifest/2 manifest")
            for line_number, line in enumerate(process.stdout, start=2):
                try:
                    record = json.loads(line)
                    identity = record["id"]
                    book_id = identity["book_id"]
                    if not first <= book_id <= last:
                        continue
                    database = record["sources"]["database"]
                    book = database["book"]
                    if not database.get("present") or book.get("file_type", "").lower() != "fb2":
                        continue
                    if identity["library"] != "flibusta":
                        raise ValueError("matching record is not from flibusta")
                    books[book_id] = record
                except (KeyError, TypeError, ValueError) as error:
                    raise ValueError(f"invalid manifest record on line {line_number}: {error}") from error
        except BaseException:
            process.terminate()
            raise
        finally:
            process.stdout.close()
        if process.wait() != 0:
            raise RuntimeError("zstd failed to decompress the manifest")
    return [books[book_id] for book_id in sorted(books)]


def validate_fb2(path):
    """Parse the whole XML, rejecting HTML pages and truncated downloads."""
    root = None
    for event, element in ET.iterparse(path, events=("start", "end")):
        if root is None:
            root = element.tag
            if root not in ("{http://www.gribuser.ru/xml/fictionbook/2.0}FictionBook", "FictionBook"):
                raise ValueError("response is not FB2 XML (possibly an HTML error page)")
        if event == "end":
            element.clear()
    if root is None:
        raise ValueError("empty FB2 response")


def download_book(book_id, output, attempts, base_url):
    destination = output / f"{book_id}.fb2"
    if destination.exists():
        validate_fb2(destination)
        print(f"  Already downloaded: {destination.name}", flush=True)
        return

    # Isolate partial files; never publish a failed HTTP response as a book.
    with tempfile.TemporaryDirectory(prefix=f".{book_id}-", dir=output) as temporary:
        payload = Path(temporary) / "response"
        subprocess.run(
            [
                "curl", "--fail", "--location", "--silent", "--show-error",
                "--proto", "=https", "--proto-redir", "=https",
                "--connect-timeout", "30", "--max-time", "300",
                "--retry", str(attempts - 1), "--retry-delay", "5",
                "--output", str(payload),
                f"{base_url}/b/{book_id}/fb2",
            ],
            check=True,
        )
        if zipfile.is_zipfile(payload):
            extracted = Path(temporary) / "book.fb2"
            with zipfile.ZipFile(payload) as archive:
                entries = [
                    entry for entry in archive.infolist()
                    if not entry.is_dir() and entry.filename.lower().endswith(".fb2")
                ]
                if len(entries) != 1:
                    raise ValueError(f"expected one FB2 in response ZIP, found {len(entries)}")
                # Copy explicitly; never trust server-provided extraction paths.
                with archive.open(entries[0]) as source, extracted.open("wb") as target:
                    shutil.copyfileobj(source, target)
            payload = extracted
        validate_fb2(payload)
        payload.replace(destination)
    print(f"  Saved: {destination.name}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path, help="database.manifest.zst path")
    parser.add_argument("--first", type=int, required=True, help="first book ID, inclusive")
    parser.add_argument("--last", type=int, required=True, help="last book ID, inclusive")
    parser.add_argument("--base-url", default="https://flibusta.site", help="HTTPS download base URL (default: %(default)s)")
    parser.add_argument("--output", type=Path, help="output directory; default: /tmp/missing-books-<random>")
    parser.add_argument("--delay", type=float, default=2, help="seconds between books (default: %(default)s)")
    parser.add_argument("--attempts", type=int, default=3, help="maximum HTTP attempts for transient errors (default: %(default)s)")
    parser.add_argument("--dry-run", action="store_true", help="list selected IDs without downloading or creating output")
    args = parser.parse_args()
    if args.first < 1 or args.last < args.first:
        parser.error("require 1 <= first <= last")
    if not math.isfinite(args.delay) or args.delay < 0 or args.attempts < 1:
        parser.error("delay must be finite and nonnegative; attempts must be positive")
    args.base_url = args.base_url.rstrip("/")
    try:
        address = urlsplit(args.base_url)
        if address.scheme != "https" or not address.hostname or address.query or address.fragment:
            parser.error("base-url must be an HTTPS URL without a query or fragment")
    except ValueError as error:
        parser.error(f"invalid base-url: {error}")
    if not args.manifest.is_file():
        parser.error(f"manifest not found: {args.manifest}")
    for command in ("zstd",) if args.dry_run else ("zstd", "curl"):
        if shutil.which(command) is None:
            parser.error(f"required command not found: {command}")

    print(f"Scanning {args.manifest} ...", flush=True)
    books = select_books(args.manifest, args.first, args.last)
    print(f"Selected {len(books)} FB2 records in {args.first}–{args.last}.", flush=True)
    if not books:
        raise ValueError("no matching FB2 records; nothing downloaded")
    if args.dry_run:
        for record in books:
            book = record["sources"]["database"]["book"]
            print(f'{record["id"]["book_id"]}\t{book.get("title", "")}')
        return 0

    output = args.output or Path(tempfile.mkdtemp(prefix="missing-books-", dir="/tmp"))
    output.mkdir(parents=True, exist_ok=True)
    output = output.resolve()
    print(f"Output directory (retained): {output}", flush=True)
    with (output / "selected-records.jsonl").open("w", encoding="utf-8") as selected:
        for record in books:
            selected.write(json.dumps(record, ensure_ascii=False) + "\n")

    failed = []
    for index, record in enumerate(books, start=1):
        book_id = record["id"]["book_id"]
        if index > 1:
            time.sleep(args.delay)
        print(f"[{index}/{len(books)}] {args.base_url}/b/{book_id}/fb2", flush=True)
        try:
            download_book(book_id, output, args.attempts, args.base_url)
        except (OSError, ValueError, ET.ParseError, zipfile.BadZipFile, RuntimeError, subprocess.CalledProcessError) as error:
            failed.append(book_id)
            print(f"  FAILED {book_id}: {error}", file=sys.stderr, flush=True)

    print(f"Finished: {len(books) - len(failed)} succeeded, {len(failed)} failed. Output: {output}", flush=True)
    if failed:
        print("Failed IDs: " + ", ".join(map(str, failed)), file=sys.stderr)
        print(f"Rerun with --output {str(output)!r} to skip valid existing FB2 files.", file=sys.stderr)
    return int(bool(failed))


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        print("\nInterrupted; completed downloads are retained.", file=sys.stderr)
        sys.exit(130)
    except (OSError, ValueError, RuntimeError) as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)
