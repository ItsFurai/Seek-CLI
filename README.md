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
seek watch              # keep the index current in the background (Ctrl+C to stop)
```

## Query syntax

Type words to fuzzy-match file names. Add any of these to narrow the results down:

| type | meaning |
|---|---|
| `report budget` | fuzzy words; all must match, and matches in the file name rank higher |
| `.pdf` · `.jpg,.png` | file type |
| `is:image` | a whole kind: `image` `video` `audio` `doc` `code` `archive` `app` (or `dir`, `file`) |
| `>10mb` · `<1kb` · `1mb..1gb` | size |
| `today` · `yesterday` · `week` · `month` · `year` | changed today, yesterday, or in the last 7 / 30 / 365 days |
| `<7d` · `>1y` | changed in the last 7 days / not changed for a year (`min h d w mo y`) |
| `photos/` | folders named like "photos" |
| `E:\work` · `~\Documents` | only inside that folder |
| `in:projects` | anywhere under a folder whose name contains "projects" |
| `'foo` · `^foo` · `foo$` · `!foo` | exact text · name starts with · path ends with · exclude |

The line under the search box shows how seek read your query, for example `"report" · PDF files · over 10 MB · changed this week`.

To search for one of the shorthand words itself, put a `'` in front: `'today`. The older `key:value` forms (`ext:pdf`, `size:>10mb`, `mod:<7d`, `in:E:\work`) still work. In a shell, quote `>` and `<` so they aren't treated as redirects: `seek find report '>10mb'`.

## Keys

`↑↓` move · `enter` open · `ctrl+o` reveal in Explorer · `ctrl+y` copy path · `tab` names ↔ contents ·
`ctrl+s` sort (relevance/newest/largest) · `ctrl+x` regex (content mode) · `ctrl+t` preview ·
`shift+↑↓` scroll preview · `ctrl+r` reindex · `F1` help · `esc` clear/quit. Mouse wheel and click work too.

## Keeping the index up to date

On Windows, the index updates itself while seek is open. It watches each drive for files being created, deleted, renamed or modified, applies changes about once a second, and shows **● live** in the header. Your selection stays put when results refresh.

- **On launch:** if the saved index is more than 30 minutes old, seek rescans in the background while you search the old copy. Changes made during the rescan are replayed afterward, so none are lost.
- **When seek is closed:** run `seek watch` to keep the index file current, so `seek find` and the next launch start fresh. It logs one summary per minute (`-v` for every batch) and saves on Ctrl+C.
- **macOS and Linux:** there's no equivalent single recursive watch, so seek rescans every 10 minutes while it's running.
- **Manual rebuild:** `ctrl+r` in the UI, or `seek index`.
## How it's fast

- **Parallel indexer:** a work-stealing pool of goroutines runs `ReadDir` on many folders at once. Windows returns size and modification time with each directory entry, so no extra `stat` call is needed per file.
- **Compact index:** paths are sorted and prefix-compressed on disk (about 29 MB for 1M entries). In memory they sit in one byte arena, so the garbage collector has almost nothing to scan.
- **Search:** every keystroke scans all entries across every core and keeps the best results in per-core top-K heaps. That takes about 20 ms for 1M entries.
- **Live updates:** a new index version shares the old one's path storage and only appends to it, so searches already running are never disturbed. A folder whose timestamp changed isn't rescanned; only new or moved-in folders are walked.
- **Content search:** a parallel grep first scans each file whole for the literal text and skips files without it. Binary files are skipped, and results stream into the UI while the search runs.

Build: `go build -ldflags="-s -w" -o seek.exe .` · Index location: `%LOCALAPPDATA%\seek\index.bin` (override with `SEEK_INDEX`).

## License

[MIT](LICENSE)
