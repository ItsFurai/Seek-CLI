# Contributing to seek

Thanks for helping out! Bug reports, ideas, docs fixes and code are all welcome.

## Reporting bugs and ideas

- **Bugs:** open a [bug report](https://github.com/ItsFurai/Seek-CLI/issues/new?template=bug_report.yml). Include your OS, `seek --version`, what you typed and what happened.
- **Ideas:** open a [feature request](https://github.com/ItsFurai/Seek-CLI/issues/new?template=feature_request.yml) and describe the problem it solves, not just the solution.
- **Security issues:** please don't open a public issue; see [SECURITY.md](SECURITY.md).

For anything larger than a small fix, open an issue first so we can agree on the approach before you spend time on it.

## Development setup

You need Go 1.24 or newer. There are no C dependencies.

```bash
git clone https://github.com/ItsFurai/Seek-CLI.git
cd Seek-CLI
go build -o seek ./cmd/seek     # seek.exe on Windows
go test ./...
```

To try a build without touching your real index, point it at a scratch file:

```bash
SEEK_INDEX=/tmp/seek-test.bin ./seek index ~/some/folder   # PowerShell: $env:SEEK_INDEX = "..."
```

## Project layout

Everything lives in `cmd/seek`:

| File | Responsibility |
|---|---|
| `main.go` | command-line commands and flags |
| `index.go` | parallel folder walk, saving and loading the index |
| `updater.go` | live index updates, rescans and saving |
| `watch_windows.go`, `watch_other.go` | file-change watching per OS |
| `query.go` | parsing the search syntax and filters |
| `match.go` | fuzzy scoring and the parallel top-results search |
| `content.go` | searching inside files |
| `preview.go` | the preview pane and syntax highlighting |
| `tui.go`, `style.go` | the terminal UI, layout, colors and keys |
| `platform_*.go` | OS-specific bits: drives, opening and revealing files |

## Before you open a pull request

CI runs these on Windows, macOS and Linux with Go 1.24 and the latest Go. Running them locally first saves a round trip:

```bash
gofmt -l .                       # should print nothing
go vet ./...
go test ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
```

Also:

- Add or update tests for behavior changes.
- Add a line to the **Unreleased** section of [CHANGELOG.md](CHANGELOG.md) for anything users will notice.
- Update the README and the `F1` help screen if you change the search syntax or keys.
- Keep pull requests focused: one change per PR is much easier to review.

## Updating the README screenshot

The screenshot is generated from a fictional demo folder, so it never shows real files:

```bash
SEEK_SCREENSHOT=$PWD/docs go test -run TestScreenshot ./cmd/seek
freeze docs/search.ansi --language ansi --window --padding 20 --margin 0 --border.radius 10 \
  --background "#1E1E2E" --font.size 14 --line-height 1.25 --output docs/screenshot.svg
rm docs/search.ansi
```

[freeze](https://github.com/charmbracelet/freeze) is installed with `go install github.com/charmbracelet/freeze@latest`.

## Releasing (maintainers)

1. Move the **Unreleased** entries in `CHANGELOG.md` under a new version heading and update the links at the bottom.
2. Commit, then tag and push: `git tag -a v1.2.3 -m "v1.2.3" && git push origin v1.2.3`.
3. The release workflow builds all platforms and publishes the release, using that version's changelog section as the notes.

## Code of conduct

Everyone taking part is expected to follow the [code of conduct](CODE_OF_CONDUCT.md).
