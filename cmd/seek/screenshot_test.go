package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestScreenshot renders the UI over a fictional demo folder and writes the
// raw ANSI frames for docs/. It only runs when asked:
//
//	SEEK_SCREENSHOT=../../docs go test -run TestScreenshot ./cmd/seek
//	freeze --language ansi docs/search.ansi -o docs/search.svg
func TestScreenshot(t *testing.T) {
	out := os.Getenv("SEEK_SCREENSHOT")
	if out == "" {
		t.Skip("set SEEK_SCREENSHOT=<dir> to regenerate the README screenshots")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	t.Setenv("SEEK_INDEX", filepath.Join(t.TempDir(), "idx.bin"))

	root := filepath.Join(t.TempDir(), "alex")
	now := time.Now()
	files := []struct {
		path string
		age  time.Duration
		body string
	}{
		{`Projects/acme-api/cmd/server/main.go`, 2 * time.Hour, demoGo},
		{`Projects/acme-api/internal/report/report.go`, 26 * time.Hour, demoReport},
		{`Projects/acme-api/internal/report/report_test.go`, 26 * time.Hour, "package report\n"},
		{`Projects/acme-api/README.md`, 72 * time.Hour, "# acme-api\n"},
		{`Projects/acme-web/src/components/ReportTable.tsx`, 5 * time.Hour, "export {}\n"},
		{`Projects/acme-web/src/pages/reports.tsx`, 30 * time.Hour, "export {}\n"},
		{`Documents/Work/Q3 Sales Report.pdf`, 3 * 24 * time.Hour, demoBlob(2_400_000)},
		{`Documents/Work/Q3 Sales Report - draft.docx`, 9 * 24 * time.Hour, demoBlob(180_000)},
		{`Documents/Work/expense-report-sept.xlsx`, 12 * 24 * time.Hour, demoBlob(64_000)},
		{`Documents/Taxes/2025/tax-report-2025.pdf`, 160 * 24 * time.Hour, demoBlob(910_000)},
		{`Pictures/Trips/Lisbon/report-photo-tram.jpg`, 40 * 24 * time.Hour, demoBlob(4_800_000)},
		{`Downloads/annual-report-2025.pdf`, 20 * 24 * time.Hour, demoBlob(7_300_000)},
		{`Downloads/report-template.zip`, 50 * 24 * time.Hour, demoBlob(1_200_000)},
		{`Music/Podcasts/weekly-report-ep42.mp3`, 6 * 24 * time.Hour, demoBlob(38_000_000)},
		{`Documents/Work/board-report-oct.pptx`, 2 * 24 * time.Hour, demoBlob(5_600_000)},
		{`Projects/acme-api/docs/reporting.md`, 4 * 24 * time.Hour, "# Reporting\n"},
		{`Desktop/report notes.txt`, 1 * time.Hour, "- check Q3 totals\n"},
		{`Pictures/Screenshots/report-dashboard.png`, 2 * 24 * time.Hour, demoBlob(820_000)},
	}
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f.path))
		write(t, p, f.body)
	}
	// Folders were just created; make them look settled so they don't flood "week".
	filepath.WalkDir(root, func(p string, d os.DirEntry, _ error) error {
		if d.IsDir() {
			old := now.Add(-60 * 24 * time.Hour)
			os.Chtimes(p, old, old)
		}
		return nil
	})
	for _, f := range files {
		mt := now.Add(-f.age)
		os.Chtimes(filepath.Join(root, filepath.FromSlash(f.path)), mt, mt)
	}
	real, err := BuildIndex([]string{root}, defaultExcludes, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Show the demo under a believable home folder instead of a temp path,
	// while the preview still reads the real temp files.
	const shown = `C:\Users\alex`
	var raw []rawEntry
	for i := range real.Entries {
		e := &real.Entries[i]
		raw = append(raw, rawEntry{path: shown + real.Path(e)[len(root):], size: e.Size, mod: e.Mod, isDir: e.IsDir()})
	}
	ix := fromRaw(raw, []string{shown}, defaultExcludes)
	previewSource = func(p string) string { return root + p[len(shown):] }
	defer func() { previewSource = func(p string) string { return p } }()

	shoot := func(name, query string, mode uiMode, selectName string) {
		var m tea.Model = newModel(ix, &Updater{Live: true}, "", mode, false)
		m = feed(m, tea.WindowSizeMsg{Width: 118, Height: 20})
		for _, r := range query {
			m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
		if mode == modeContent {
			time.Sleep(500 * time.Millisecond) // let the debounced grep stream in
			m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
			m = feed(m, tea.KeyMsg{Type: tea.KeyBackspace})
			time.Sleep(500 * time.Millisecond)
			m = feed(m, tickMsg{})
		}
		for i := 0; i < 20 && filepath.Base(m.(model).selectedPathV()) != selectName; i++ {
			m = feed(m, tea.KeyMsg{Type: tea.KeyDown})
		}
		if err := os.WriteFile(filepath.Join(out, name+".ansi"), []byte(m.View()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	shoot("search", "report week", modeName, "report.go")
}

func demoBlob(n int) string { return string(make([]byte, n)) }

const demoReport = `// Package report serves sales reports over HTTP.
package report

import (
	"encoding/json"
	"net/http"
	"time"
)

type Report struct {
	ID      string
	Title   string
	Total   float64
	Created time.Time
}

// Get returns one report by ID.
func Get(w http.ResponseWriter, r *http.Request) {
	rep, err := store.Find(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, "report not found", http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(rep)
}
`

const demoGo = `package main

import (
	"log"
	"net/http"

	"acme/internal/report"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /reports/{id}", report.Get)
	mux.HandleFunc("POST /reports", report.Create)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
`

func (m model) selectedPathV() string { return m.selectedPath() }
