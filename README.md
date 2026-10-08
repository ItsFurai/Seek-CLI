# Seek-CLI

Fast file search and indexing with a terminal UI, written in Go. The command is `seek`.

## Install

**Download:** get a ready-to-run build for Windows, macOS or Linux from [Releases](https://github.com/ItsFurai/Seek-CLI/releases/latest), unzip it, and put the folder on your PATH. On macOS, run `xattr -d com.apple.quarantine seek` the first time, because the builds aren't signed.

**Build from source:** needs Go 1.24 or newer. There are no C dependencies, so it builds the same way on Windows, macOS and Linux (x86-64 and ARM64).

```bash
git clone https://github.com/ItsFurai/Seek-CLI.git
cd Seek-CLI
go build -ldflags="-s -w" -o seek.exe .
```

Then put the folder on your PATH. On macOS and Linux, build with `-o seek` instead of `-o seek.exe`.

Platform notes:
- With no arguments, `seek index` indexes every fixed drive on Windows and your home folder on macOS and Linux.
- On Linux, copying a path (`ctrl+y`) needs `xclip`, `xsel` or `wl-clipboard` installed.

## Usage

```
seek                    # open the UI (first run builds the index automatically)
seek report ext:pdf     # open the UI with a query already typed
seek -c TODO in:E:\work # open the UI in content (grep) mode
cd (seek pick)          # PowerShell: pick a folder and cd into it
seek index [dirs...]    # (re)build the index; default is every fixed drive
seek find <query>       # print matching paths (scriptable)
seek grep <text> ext:go # search inside files from the shell
seek stats
```

## Query syntax

| query | meaning |
|---|---|
| `foo bar` | fuzzy; every term must match. Matches in the file name rank higher |
| `'foo` / `^foo` / `foo$` / `!foo` | exact substring / name starts with / path ends with / exclude |
| `ext:go,rs` | extensions |
| `is:dir`, `is:file` | entry type |
| `size:>10mb`, `size:1mb..1gb` | size |
| `mod:<7d`, `mod:>1y` | modified within / older than (`min h d w mo y`) |
| `in:projects`, `in:E:\work` | folder name in the path / path prefix |

## Keys

`↑↓` move · `enter` open · `ctrl+o` reveal in Explorer · `ctrl+y` copy path · `tab` names ↔ contents ·
`ctrl+s` sort (relevance/newest/largest) · `ctrl+x` regex (content mode) · `ctrl+t` preview ·
`shift+↑↓` scroll preview · `ctrl+r` reindex · `F1` help · `esc` clear/quit. Mouse wheel and click work too.

## How it's fast

- **Parallel indexer:** a work-stealing pool of goroutines runs `ReadDir` on many folders at once. Windows returns size and modification time with each directory entry, so no extra `stat` call is needed per file.
- **Compact index:** paths are sorted and prefix-compressed on disk (about 29 MB for 1M entries). In memory they sit in one byte arena, so the garbage collector has almost nothing to scan.
- **Search:** every keystroke scans all entries across every core and keeps the best results in per-core top-K heaps. That takes about 20 ms for 1M entries.
- **Content search:** a parallel grep first scans each file whole for the literal text and skips files without it. Binary files are skipped, and results stream into the UI while the search runs.

Build: `go build -ldflags="-s -w" -o seek.exe .` · Index location: `%LOCALAPPDATA%\seek\index.bin` (override with `SEEK_INDEX`).
