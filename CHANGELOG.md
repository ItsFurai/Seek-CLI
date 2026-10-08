# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **Simpler query shorthands** that need no `key:` prefix: `.pdf` for file types, `>10mb` / `<1kb` / `1mb..1gb` for size, `<7d` / `>1y` for dates, `today`, `yesterday`, `week`, `month` and `year`, `photos/` for folders, and `E:\work` or `~\Documents` to search inside a folder.
- **File kinds:** `is:image`, `is:video`, `is:audio`, `is:doc`, `is:code`, `is:archive` and `is:app`, using the same groups as the result colors.
- **The search box explains your query** in its bottom border (for example `"report" · PDF files · over 10 MB · changed this week`), and shows errors in the same place.

### Changed
- A folder filter such as `E:\work` now matches only that folder, not `E:\workshop`.
- Extension filters also match dotfiles, so `.gitignore` finds files named `.gitignore`.
- The words `today`, `yesterday`, `week`, `month` and `year` are now filters; put a `'` in front (`'today`) to search for the word itself.

## [0.2.0] - 2026-10-08

### Added
- **Live index updates on Windows.** While seek is open, it watches each drive for files being created, deleted, renamed or modified, and applies changes about once a second. The header shows **● live**, and the selection stays on the same file when results refresh.
- **Catch-up on launch.** A saved index older than 30 minutes is rescanned in the background while you search the old copy. Changes made during the rescan are replayed afterward.
- `seek watch` keeps the index file current without the UI. It logs a summary once a minute (`-v` for every batch) and saves on Ctrl+C.
- Periodic background rescans (every 10 minutes) on macOS and Linux, which have no single recursive watch.
- An automatic full rescan if Windows reports more changes than it can deliver.
- An MIT license, also included in each release download.

### Changed
- The index is saved every 30 seconds after live changes and when seek exits.
- `ctrl+r` in the UI now goes through the same background updater as live changes.
- GitHub Actions moved to their Node 24 versions, and CI now also runs Go's race detector on Linux.

## [0.1.0] - 2026-10-08

### Added
- Interactive terminal UI with a results list, a syntax-highlighted preview, mouse support and a help screen (`F1`).
- Parallel indexer for all fixed drives (Windows) or the home folder (macOS/Linux), with a compact prefix-compressed index file.
- Fuzzy name search across every CPU core, with filters: `ext:`, `is:`, `size:`, `mod:`, `in:`, and the `'exact`, `^prefix`, `suffix$` and `!exclude` operators.
- Content search (`tab`): parallel grep with smart case, optional regex (`ctrl+x`), binary-file skipping and streaming results.
- Actions: open (`enter`), reveal in Explorer/Finder (`ctrl+o`), copy path (`ctrl+y`), sort by relevance/newest/largest (`ctrl+s`).
- Commands: `seek find`, `seek grep`, `seek pick` (for `cd (seek pick)`), `seek index`, `seek stats`, `seek --version`.
- Prebuilt downloads for Windows, macOS and Linux (x86-64 and ARM64) with checksums.
- Works with Go 1.24 or newer, and CI builds and tests on Windows, macOS and Linux.

[Unreleased]: https://github.com/ItsFurai/Seek-CLI/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/ItsFurai/Seek-CLI/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/ItsFurai/Seek-CLI/releases/tag/v0.1.0
