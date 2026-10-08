package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// feed sends msg to the model and synchronously resolves the commands it returns
// (skipping timers), which is enough to drive searches and previews in tests.
func feed(m tea.Model, msg tea.Msg) tea.Model {
	m, cmd := m.Update(msg)
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		done := make(chan tea.Msg, 1)
		go func() { done <- c() }()
		var out tea.Msg
		select {
		case out = <-done:
		case <-time.After(200 * time.Millisecond):
			continue // a timer
		}
		switch o := out.(type) {
		case tea.BatchMsg:
			queue = append(queue, o...)
		case tickMsg, flashClearMsg, nil:
		default:
			var next tea.Cmd
			m, next = m.Update(o)
			queue = append(queue, next)
		}
	}
	return m
}

func TestTUILiveRefreshKeepsSelection(t *testing.T) {
	ix := testIndex(t)
	var m tea.Model = newModel(ix, &Updater{Live: true}, "", modeName, false)
	m = feed(m, tea.WindowSizeMsg{Width: 120, Height: 20})
	for _, r := range "go" {
		m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = feed(m, tea.KeyMsg{Type: tea.KeyDown})
	mm := m.(model)
	before := mm.selectedPath()
	if before == "" {
		t.Fatal("nothing selected")
	}

	// a new matching file appears; the list refreshes but the cursor stays put
	dir := filepath.Dir(filepath.Dir(before))
	write(t, filepath.Join(dir, "aaa_go_new.go"), "x")
	nx, _ := ix.applyChanges(map[string]bool{filepath.Join(dir, "aaa_go_new.go"): true}, excludeSet(defaultExcludes))
	m = feed(m, indexUpdatedMsg{ix: nx, changes: 1})

	mm = m.(model)
	if got := mm.selectedPath(); got != before {
		t.Errorf("selection moved from %s to %s", before, got)
	}
	view := ansi.Strip(mm.View())
	if !strings.Contains(view, "aaa_go_new.go") || !strings.Contains(view, "live") {
		t.Errorf("refreshed view missing new file or live badge:\n%s", view)
	}
}
