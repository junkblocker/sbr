# SMS Backup and Restore Tools

MMS attachment extractor for backups produced by the
[SMS Backup & Restore](https://www.synctech.com.au/sms-backup-restore/) Android app.

## Usage

```
sbr [-d 0|1|2|3] <input-file-or-directory> <output-directory>
```

- `input` — a single `sms-*.xml` backup file, or a directory that is walked
  recursively for all matching files.
- `output` — directory where extracted attachments are written (created if it
  does not exist).
- `-d` — debug verbosity level (0 = quiet, 3 = very verbose).

## Output filenames

Attachments are named `<date>-<leaf>`, where `<date>` is the MMS timestamp
formatted as `YYYY-MM-DD-HHMMSS` in local time and `<leaf>` is derived by the
following priority:

1. The `cl` (Content-Location) attribute of the `<part>` element.
2. The `name` attribute of the `<part>` element.
3. A synthesised name: `<partIndex><ext>`, where `<partIndex>` is the
   zero-based position of the part within its `<mms>` element and `<ext>` is
   derived from the MIME content type.

When two MMS messages would produce the same output path (same timestamp to
the second, same leaf name), the first message keeps the natural name and each
subsequent collision gets `<date>-<sha256[0:8]>-<leaf>` — a stable, content-
derived prefix that is identical across full and incremental backup files
containing the same attachment.

## Full + incremental backup sets

SMS Backup & Restore produces overlapping files: incremental backups contain
recent messages, full backups are complete snapshots. Running `sbr` against a
directory that contains both is safe and idempotent:

- Attachments already extracted from an incremental are skipped (by filename)
  when the same message reappears in a subsequent full backup.
- The hash-based collision disambiguator is derived from attachment content, not
  from file-relative position, so the same image always maps to the same output
  filename regardless of which file it comes from.
- Re-running `sbr` against the same input set never overwrites or duplicates
  existing output files.

## Concurrency model

Each `sms-*.xml` file is parsed in its own goroutine. Within each file, a
bounded pool of `2 × GOMAXPROCS` workers handles the I/O concurrently. All
writes complete before the process exits.

Atomic writes are guaranteed: each attachment is first written to a uniquely
named temp file in the output directory (`.sbr-*.tmp`), timestamped, then
renamed into place. Concurrent writes to the same final path (e.g. the same
attachment in both an incremental and a full backup) are safe — the rename is
last-writer-wins for identical content, and distinct content is separated by
the hash disambiguator before it ever reaches the rename step.
