# Flibusta Database Sequence Notes

This document describes how Flibusta SQL dumps model book series, also called sequences.

The relevant dump files are published under `https://flibusta.is/sql/` as `lib.libseq.sql.gz` and `lib.libseqname.sql.gz`.

## Tables

Series names are stored once in `libseqname`:

```sql
CREATE TABLE `libseqname` (
  `SeqId` int(10) unsigned NOT NULL AUTO_INCREMENT,
  `SeqName` varchar(254) COLLATE utf8_unicode_ci NOT NULL DEFAULT '',
  PRIMARY KEY (`SeqId`),
  UNIQUE KEY `SeqName_2` (`SeqName`)
) ENGINE=MyISAM DEFAULT CHARSET=utf8 COLLATE=utf8_unicode_ci;
```

Book-to-series links are stored in `libseq`:

```sql
CREATE TABLE `libseq` (
  `BookId` int(11) NOT NULL,
  `SeqId` int(11) NOT NULL,
  `SeqNumb` int(11) NOT NULL,
  `Level` tinyint(4) NOT NULL DEFAULT '0',
  `Type` tinyint(1) unsigned NOT NULL DEFAULT '0',
  PRIMARY KEY (`BookId`,`SeqId`),
  KEY `SeqId` (`SeqId`)
) ENGINE=MyISAM DEFAULT CHARSET=latin1;
```

The tables are MyISAM and do not declare foreign keys. Treat `libseq.SeqId -> libseqname.SeqId` and `libseq.BookId -> libbook.BookId` as conventional references.

## `libseq` Columns

- `BookId`: book identifier from `libbook`.
- `SeqId`: series identifier from `libseqname`.
- `SeqNumb`: book number inside the series. A value of `0` usually means unknown, absent, or not applicable.
- `Level`: flattened sequence hierarchy/source level.
- `Type`: sequence class. Current Flibusta dumps use `0` and `1`.

The primary key is `(BookId, SeqId)`, so one book can belong to many sequences, but cannot have two separate rows for the same sequence id.

## Sequence Types

Current Flibusta data uses these `Type` values:

- `Type = 0`: author/book sequence. This corresponds to FB2 `<description><title-info><sequence ...>` metadata.
- `Type = 1`: publisher sequence. This corresponds to FB2 `<description><publish-info><sequence ...>` metadata.

The classic `lib2inpx` behavior is to select one sequence for INPX output:

- `author` mode sorts by `Type ASC, Level ASC` and therefore prefers `Type = 0`.
- `publisher` mode sorts by `Type DESC, Level ASC` and therefore prefers `Type = 1`.

This repository follows the same preference model for `mhl-inpx`, but preserves all database sequences in JSON metadata.

## Hierarchy Model

Flibusta does not store sequence hierarchy as an adjacency list. There is no parent sequence id column.

Hierarchy is flattened into multiple `libseq` rows for the same book. `Level` records how deeply the sequence appeared in FB2 metadata, with a special offset for publisher metadata.

The historical parser convention is:

- When entering `<title-info>`, sequence level starts at `0`.
- Each nested `<sequence>` increments the level.
- Top-level title-info sequence is usually stored as `Level = 1`.
- Nested title-info sequence is usually stored as `Level = 2`, and so on.
- When entering `<publish-info>`, sequence level starts at `100`.
- Top-level publish-info sequence is usually stored as `Level = 101`.
- Nested publish-info sequence is usually stored as `Level = 102`, and so on.

Example FB2 title-info metadata:

```xml
<title-info>
  <sequence name="Outer Cycle" number="0">
    <sequence name="Inner Cycle" number="1"/>
  </sequence>
</title-info>
```

Conceptual database rows:

```text
BookId  SeqId(Outer Cycle)  SeqNumb=0  Level=1  Type=0
BookId  SeqId(Inner Cycle)  SeqNumb=1  Level=2  Type=0
```

Example FB2 publish-info metadata:

```xml
<publish-info>
  <sequence name="Publisher Line" number="5"/>
</publish-info>
```

Conceptual database row:

```text
BookId  SeqId(Publisher Line)  SeqNumb=5  Level=101  Type=1
```

## Important Data Caveats

`Level` should be treated as an ordering and grouping hint, not as a perfect tree representation.

Reasons:

- `Level` has a default value of `0`, and real dumps contain many `Level = 0` rows.
- Historical manual edits may have inserted rows without preserving parser-derived levels.
- The schema prevents duplicate `(BookId, SeqId)` rows, so the same series cannot represent two different hierarchy positions for the same book.
- Without parent ids, rows with adjacent levels only imply source nesting order; they do not encode an explicit parent-child edge.

For title-info sequences, consumers often ignore publisher levels by filtering `Level < 100`. The historical Librusec display code did this when showing book rows.

## Recommended Query

To fetch all database sequences for one book:

```sql
SELECT sn.SeqId, sn.SeqName, s.SeqNumb, s.Level, s.Type
  FROM libseq s
  JOIN libseqname sn ON sn.SeqId = s.SeqId
 WHERE s.BookId = ?
 ORDER BY s.Type, s.Level, sn.SeqName;
```

To prefer author/title-info series, use ascending `Type`:

```sql
ORDER BY s.Type ASC, s.Level ASC, sn.SeqName ASC
```

To prefer publisher series, use descending `Type`:

```sql
ORDER BY s.Type DESC, s.Level ASC, sn.SeqName ASC
```

## Current Project Behavior

`metabib` reads all sequence rows into `model.DatabaseSource.Sequences`.

The JSON model preserves:

- `id`: `libseqname.SeqId`.
- `name`: `libseqname.SeqName`.
- `number`: `libseq.SeqNumb`.
- `level`: `libseq.Level`.
- `type`: `libseq.Type`.

The INPX exporter must collapse this to one `SERIES` and one `SERNO`, because classic INPX rows have only one series field. It sorts according to `--sequence author|publisher|ignore` and selects the first row after sorting.

## Practical Interpretation

For metadata preservation, keep every `libseq` row.

For display or classic INPX export, choose one row according to the desired preference:

- Prefer author series: sort by `Type ASC, Level ASC`.
- Prefer publisher series: sort by `Type DESC, Level ASC`.
- Ignore series: do not emit database sequence metadata.

For hierarchy-aware consumers, reconstruct only a best-effort path from rows ordered by `Type`, then `Level`. Do not assume this is a strict tree unless the data source is known to have been generated directly from nested FB2 sequence tags.
