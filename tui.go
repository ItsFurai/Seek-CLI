package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

type uiMode int

const (
	modeName uiMode = iota
	modeContent
)

const (
	nameLimit    = 2000
	contentLimit = 5000
)

var prog *tea.Program

// ---------- messages ----------

type searchDoneMsg struct {
	seq   int
	ix    *Index
	hits  []Hit
	total int
	took  time.Duration
	keep  string // path to keep selected (live refresh)
}
type contentStartMsg struct{ seq int }
type contentBatchMsg struct {
	seq  int
	hits []ContentHit
}
type contentDoneMsg struct {
	seq  int
	err  error
	took time.Duration
}
type previewMsg struct{ pd previewData }
type indexStartMsg struct{ progress *atomic.Int64 }
type indexUpdatedMsg struct {
	ix      *Index
	changes int
	full    bool
}
type indexErrMsg struct{ err error }
type tickMsg struct{}
type flashClearMsg struct{ seq int }

// ---------- model ----------

type model struct {
	ix    *Index
	input textinput.Model
	mode  uiMode
	sort  SortMode
	regex bool
	w, h  int
	q     *Query
	seq   int

	// Each result list remembers the index version its Idx values refer to,
	// because live updates swap m.ix underneath them.
	hits   []Hit
	hitsIx *Index
	total  int
	took   time.Duration

	chits    []ContentHit
	chitsIx  *Index
	cScanned *atomic.Int64
	cCancel  context.CancelFunc
	cRunning bool
	cStart   time.Time
	cTook    time.Duration
	cErr     string

	cursor, offset int

	showPreview bool
	pv          previewData
	pvScroll    int
	pvCache     map[string]previewData
	pvOrder     []string
	pvStale     bool

	upd         *Updater
	indexing    bool
	idxProgress *atomic.Int64
	idxStart    time.Time
	ticking     bool
	frame       int

	flash    string
	flashErr bool
	flashSeq int
	showHelp bool

	pick    bool
	picked  string
	initCmd tea.Cmd
}

func newModel(ix *Index, upd *Updater, initial string, mode uiMode, pick bool) model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = "search files…   try: report ext:pdf mod:<30d   ·   tab: search inside files"
	ti.PlaceholderStyle = sFaint
	ti.TextStyle = lipgloss.NewStyle().Foreground(cText)
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(cAccent)
	ti.SetValue(initial)
	ti.Focus()
	m := model{
		ix: ix, upd: upd, input: ti, mode: mode, showPreview: true, pick: pick,
		pvCache: map[string]previewData{}, pv: previewData{focus: -1},
		q: ParseQuery(initial, time.Now()), idxProgress: &atomic.Int64{},
	}
	if ix == nil {
		m.indexing, m.idxStart = true, time.Now() // the updater builds it
		m.initCmd = m.ensureTick()
	} else {
		m.initCmd = m.querySeq(m.seq)
	}
	return m
}

func (m model) Init() tea.Cmd { return tea.Batch(textinput.Blink, m.initCmd) }

func (m *model) ensureTick() tea.Cmd {
	if m.ticking {
		return nil
	}
	m.ticking = true
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) setFlash(s string, isErr bool) tea.Cmd {
	m.flash, m.flashErr = s, isErr
	m.flashSeq++
	seq := m.flashSeq
	return tea.Tick(2500*time.Millisecond, func(time.Time) tea.Msg { return flashClearMsg{seq} })
}

// querySeq kicks off a search for the current input.
func (m *model) querySeq(seq int) tea.Cmd { return m.querySeqKeep(seq, "") }

func (m *model) querySeqKeep(seq int, keep string) tea.Cmd {
	m.q = ParseQuery(m.input.Value(), time.Now())
	if m.ix == nil {
		return nil
	}
	if m.mode == modeName {
		ix, q, sortMode := m.ix, m.q, m.sort
		return func() tea.Msg {
			start := time.Now()
			hits, total := Search(ix, q, sortMode, nameLimit)
			return searchDoneMsg{seq: seq, ix: ix, hits: hits, total: total, took: time.Since(start), keep: keep}
		}
	}
	m.cancelContent()
	m.chits = nil
	m.cErr = ""
	m.cursor, m.offset = 0, 0
	if strings.TrimSpace(m.q.Text()) == "" {
		return m.previewCmd()
	}
	return tea.Tick(180*time.Millisecond, func(time.Time) tea.Msg { return contentStartMsg{seq} })
}

func (m *model) cancelContent() {
	if m.cCancel != nil {
		m.cCancel()
		m.cCancel = nil
	}
	m.cRunning = false
}

func (m *model) startContent(seq int) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.cCancel = cancel
	m.cRunning = true
	m.cStart = time.Now()
	m.cScanned = &atomic.Int64{}
	m.chitsIx = m.ix
	ix, q, pat, re, scanned := m.ix, m.q, m.q.Text(), m.regex, m.cScanned
	go func() {
		start := time.Now()
		err := Grep(ctx, ix, q, pat, re, contentLimit, func(b []ContentHit) {
			prog.Send(contentBatchMsg{seq: seq, hits: b})
		}, scanned)
		prog.Send(contentDoneMsg{seq: seq, err: err, took: time.Since(start)})
	}()
	return m.ensureTick()
}

func (m *model) count() int {
	if m.mode == modeName {
		return len(m.hits)
	}
	return len(m.chits)
}

// listIx is the index version the visible result list refers to.
func (m *model) listIx() *Index {
	if m.mode == modeName {
		return m.hitsIx
	}
	return m.chitsIx
}

// selected returns the current entry and focus line (content mode).
func (m *model) selected() (*Entry, int) {
	ix := m.listIx()
	if ix == nil {
		return nil, 0
	}
	if m.mode == modeName {
		if m.cursor < len(m.hits) {
			return &ix.Entries[m.hits[m.cursor].Idx], 0
		}
	} else if m.cursor < len(m.chits) {
		h := m.chits[m.cursor]
		return &ix.Entries[h.Idx], h.Line
	}
	return nil, 0
}

func (m *model) selectedPath() string {
	if e, _ := m.selected(); e != nil {
		return m.listIx().Path(e)
	}
	return ""
}

func (m *model) previewCmd() tea.Cmd {
	e, line := m.selected()
	if e == nil || !m.showPreview {
		m.pv = previewData{focus: -1}
		return nil
	}
	path := m.listIx().Path(e)
	key := previewKey(path, line)
	if m.pv.key == key && !m.pvStale {
		return nil
	}
	if pd, ok := m.pvCache[key]; ok {
		m.pv = pd
		m.resetPvScroll()
		return nil
	}
	isDir := e.IsDir()
	return func() tea.Msg { return previewMsg{loadPreview(path, isDir, line)} }
}

func (m *model) resetPvScroll() {
	m.pvScroll = 0
	if m.pv.focus >= 0 {
		m.pvScroll = max(0, m.pv.focus-m.previewBodyHeight()/3)
	}
}

func (m *model) moveCursor(d int) tea.Cmd {
	n := m.count()
	if n == 0 {
		return nil
	}
	m.cursor = max(0, min(n-1, m.cursor+d))
	m.clampOffset()
	return m.previewCmd()
}

func (m *model) clampOffset() {
	body := max(1, m.listBodyHeight())
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+body {
		m.offset = m.cursor - body + 1
	}
}

// ---------- update ----------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.input.Width = max(10, m.w-8)
		return m, nil

	case tickMsg:
		m.frame++
		if m.indexing || m.cRunning {
			return m, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
		}
		m.ticking = false
		return m, nil

	case flashClearMsg:
		if msg.seq == m.flashSeq {
			m.flash = ""
		}
		return m, nil

	case indexStartMsg:
		m.indexing, m.idxProgress, m.idxStart = true, msg.progress, time.Now()
		return m, m.ensureTick()

	case indexErrMsg:
		m.indexing = false
		return m, m.setFlash("index: "+msg.err.Error(), true)

	case indexUpdatedMsg:
		first := m.ix == nil
		m.ix = msg.ix
		var cmds []tea.Cmd
		if msg.full {
			m.indexing = false
			f, d := m.ix.Files()
			cmds = append(cmds, m.setFlash(fmt.Sprintf("index refreshed: %s files, %s folders in %s",
				commas(f), commas(d), time.Since(m.idxStart).Round(100*time.Millisecond)), false))
		}
		// Changed files may be on screen: drop cached previews, but keep showing
		// the current one until its replacement loads (no flicker).
		m.pvCache, m.pvOrder, m.pvStale = map[string]previewData{}, nil, true
		// Re-run name searches in place; content results stay until the next query.
		if m.mode == modeName || first {
			m.seq++
			cmds = append(cmds, m.querySeqKeep(m.seq, m.selectedPath()))
		}
		return m, tea.Batch(cmds...)

	case searchDoneMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.hits, m.hitsIx, m.total, m.took = msg.hits, msg.ix, msg.total, msg.took
		m.cursor, m.offset = 0, 0
		if msg.keep != "" {
			for i, h := range m.hits {
				if msg.ix.Path(&msg.ix.Entries[h.Idx]) == msg.keep {
					m.cursor = i
					break
				}
			}
			m.clampOffset()
		} else {
			m.pv.key = ""
		}
		return m, m.previewCmd()

	case contentStartMsg:
		if msg.seq != m.seq || m.mode != modeContent {
			return m, nil
		}
		return m, m.startContent(msg.seq)

	case contentBatchMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		first := len(m.chits) == 0
		m.chits = append(m.chits, msg.hits...)
		if first {
			m.pv.key = ""
			return m, m.previewCmd()
		}
		return m, nil

	case contentDoneMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.cRunning = false
		m.cTook = msg.took
		if msg.err != nil {
			m.cErr = msg.err.Error()
		}
		return m, nil

	case previewMsg:
		m.pvCache[msg.pd.key] = msg.pd
		m.pvOrder = append(m.pvOrder, msg.pd.key)
		if len(m.pvOrder) > 64 {
			delete(m.pvCache, m.pvOrder[0])
			m.pvOrder = m.pvOrder[1:]
		}
		if e, line := m.selected(); e != nil && previewKey(m.listIx().Path(e), line) == msg.pd.key {
			if !(m.pvStale && m.pv.key == msg.pd.key) {
				m.resetPvScroll() // keep the scroll position on a live refresh
			}
			m.pv, m.pvStale = msg.pd, false
		}
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	inPreview := m.showPreview && msg.X >= m.listWidth()
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if inPreview {
			m.pvScroll = max(0, m.pvScroll-3)
			return m, nil
		}
		return m, m.moveCursor(-3)
	case tea.MouseButtonWheelDown:
		if inPreview {
			m.pvScroll = min(max(0, len(m.pv.lines)-1), m.pvScroll+3)
			return m, nil
		}
		return m, m.moveCursor(3)
	case tea.MouseButtonLeft:
		row := msg.Y - m.listTop() - 1
		if !inPreview && row >= 0 && row < m.listBodyHeight() {
			i := m.offset + row
			if i < m.count() {
				if i == m.cursor {
					return m.openSelected(false)
				}
				m.cursor = i
				return m, m.previewCmd()
			}
		}
	}
	return m, nil
}

func (m model) openSelected(reveal bool) (tea.Model, tea.Cmd) {
	e, _ := m.selected()
	if e == nil {
		return m, nil
	}
	path := m.listIx().Path(e)
	if m.pick {
		m.picked = path
		return m, tea.Quit
	}
	var err error
	verb := "opened"
	if reveal {
		err, verb = revealPath(path), "revealed"
	} else {
		err = openPath(path)
	}
	if err != nil {
		return m, m.setFlash(err.Error(), true)
	}
	return m, m.setFlash(verb+" "+filepath.Base(path), false)
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c":
		m.cancelContent()
		return m, tea.Quit
	case "esc":
		if m.input.Value() != "" {
			m.input.SetValue("")
			m.seq++
			return m, m.querySeq(m.seq)
		}
		m.cancelContent()
		return m, tea.Quit
	case "f1":
		m.showHelp = true
		return m, nil
	case "?":
		if m.input.Value() == "" {
			m.showHelp = true
			return m, nil
		}
	case "up", "ctrl+p":
		return m, m.moveCursor(-1)
	case "down", "ctrl+n":
		return m, m.moveCursor(1)
	case "pgup":
		return m, m.moveCursor(-m.listBodyHeight())
	case "pgdown":
		return m, m.moveCursor(m.listBodyHeight())
	case "shift+up":
		m.pvScroll = max(0, m.pvScroll-1)
		return m, nil
	case "shift+down":
		m.pvScroll = min(max(0, len(m.pv.lines)-1), m.pvScroll+1)
		return m, nil
	case "enter":
		return m.openSelected(false)
	case "ctrl+o":
		return m.openSelected(true)
	case "ctrl+y":
		if e, line := m.selected(); e != nil {
			p := m.listIx().Path(e)
			if line > 0 {
				p = fmt.Sprintf("%s:%d", p, line)
			}
			if err := clipboard.WriteAll(p); err != nil {
				return m, m.setFlash("clipboard: "+err.Error(), true)
			}
			return m, m.setFlash("copied "+p, false)
		}
		return m, nil
	case "tab":
		if m.mode == modeName {
			m.mode = modeContent
		} else {
			m.cancelContent()
			m.mode = modeName
		}
		m.cursor, m.offset = 0, 0
		m.pv.key = ""
		m.seq++
		return m, m.querySeq(m.seq)
	case "ctrl+s":
		if m.mode == modeName {
			m.sort = (m.sort + 1) % sortModes
			m.seq++
			return m, tea.Batch(m.querySeq(m.seq), m.setFlash("sort: "+m.sort.String(), false))
		}
		return m, nil
	case "ctrl+x":
		m.regex = !m.regex
		state := "off"
		if m.regex {
			state = "on"
		}
		cmds := []tea.Cmd{m.setFlash("regex "+state, false)}
		if m.mode == modeContent {
			m.seq++
			cmds = append(cmds, m.querySeq(m.seq))
		}
		return m, tea.Batch(cmds...)
	case "ctrl+t":
		m.showPreview = !m.showPreview
		m.pv.key = ""
		return m, m.previewCmd()
	case "ctrl+r":
		if !m.indexing {
			m.upd.Rebuild()
		}
		return m, nil
	}

	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		m.seq++
		return m, tea.Batch(cmd, m.querySeq(m.seq))
	}
	return m, cmd
}

// ---------- layout ----------

func (m *model) listTop() int        { return 4 } // header(1) + input box(3)
func (m *model) mainHeight() int     { return max(3, m.h-m.listTop()-1) }
func (m *model) listBodyHeight() int { return m.mainHeight() - 2 }
func (m *model) previewBodyHeight() int {
	return max(1, m.mainHeight()-2-2) // minus meta lines
}
func (m *model) listWidth() int {
	if !m.showPreview || m.w < 70 {
		return m.w
	}
	return m.w * 52 / 100
}

// ---------- view ----------

func (m model) View() string {
	if m.w == 0 {
		return ""
	}
	if m.showHelp {
		return m.helpView()
	}
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteByte('\n')
	b.WriteString(m.inputView())
	b.WriteByte('\n')

	lw := m.listWidth()
	list := m.listView(lw)
	if lw < m.w {
		list = lipgloss.JoinHorizontal(lipgloss.Top, list, m.previewView(m.w-lw))
	}
	b.WriteString(list)
	b.WriteByte('\n')
	b.WriteString(m.footerView())
	return b.String()
}

func (m *model) headerView() string {
	left := sLogo.Render("seek") + "  "
	tab := func(label string, on bool) string {
		if on {
			return sTabOn.Render(label)
		}
		return sTabOff.Render(label)
	}
	left += tab("Names", m.mode == modeName) + sFaint.Render("  │  ") + tab("Contents", m.mode == modeContent)

	var right string
	switch {
	case m.indexing:
		verb := "refreshing"
		if m.ix == nil {
			verb = "indexing"
		}
		right = sKey.Render(spinner(m.frame)) + " " + sDim.Render(fmt.Sprintf("%s… %s items · %s ", verb,
			commas(int(m.idxProgress.Load())), time.Since(m.idxStart).Round(time.Second)))
	case m.ix != nil && m.upd != nil && m.upd.Live:
		right = sDim.Render(commas(len(m.ix.Entries))+" items · ") + sOK.Render("● live ")
	case m.ix != nil:
		right = sDim.Render(fmt.Sprintf("%s items · indexed %s ", commas(len(m.ix.Entries)), humanAge(m.ix.Created)))
	}
	return padBetween(left, right, m.w)
}

func (m *model) inputView() string {
	inner := m.w - 2
	prompt := lipgloss.NewStyle().Foreground(cAccent).Bold(true).Render("❯ ")
	var chips []string
	if m.q != nil {
		if m.q.Err != "" {
			chips = append(chips, sErr.Render("⚠ "+m.q.Err))
		} else if m.q.HasFilters() {
			chips = append(chips, sKey.Render("⧩ filtered"))
		}
	}
	if m.regex {
		chips = append(chips, sKey.Render(".* regex"))
	}
	chip := strings.Join(chips, "  ")
	m.input.Width = max(5, inner-4-lipgloss.Width(chip)-2)
	line := padBetween(" "+prompt+m.input.View(), chip+" ", inner)
	return drawBox("", "", []string{line}, m.w, 3, true)
}

func (m *model) listView(w int) string {
	h := m.mainHeight()
	body := h - 2
	inner := w - 2
	n := m.count()

	m.clampOffset()

	var lines []string
	switch {
	case m.ix == nil && m.indexing:
		lines = centerMsg(body, inner, sDim.Render("building your index for the first time…"),
			sFaint.Render(fmt.Sprintf("%s items so far", commas(int(m.idxProgress.Load())))))
	case n == 0:
		msg := "no matches"
		if m.mode == modeContent {
			switch {
			case strings.TrimSpace(m.q.Text()) == "":
				msg = "type text to search inside files (use ext: / in: to narrow it down)"
			case m.cRunning:
				msg = "searching…"
			case m.cErr != "":
				msg = m.cErr
			}
		}
		lines = centerMsg(body, inner, sDim.Render(msg))
	default:
		for i := m.offset; i < min(n, m.offset+body); i++ {
			if m.mode == modeName {
				lines = append(lines, m.nameRow(i, inner))
			} else {
				lines = append(lines, m.contentRow(i, inner))
			}
		}
	}

	title := "Results"
	if m.mode == modeContent {
		title = "Matches"
	}
	pos := ""
	if n > 0 {
		pos = fmt.Sprintf("%d/%d", m.cursor+1, n)
	}
	return drawBox(title, pos, lines, w, h, true)
}

func rowStyle(sel bool) lipgloss.Style {
	if sel {
		return lipgloss.NewStyle().Background(cSelBg)
	}
	return lipgloss.NewStyle()
}

func (m *model) nameRow(i, w int) string {
	ix := m.hitsIx
	e := &ix.Entries[m.hits[i].Idx]
	sel := i == m.cursor
	bg := rowStyle(sel)
	path := ix.Path(e)
	name := path[e.Base:]
	cat := categoryOf(name, e.IsDir())

	var b strings.Builder
	if sel {
		b.WriteString(bg.Foreground(cAccent).Render("▌"))
	} else {
		b.WriteString(" ")
	}
	b.WriteString(bg.Foreground(cat.color).Render(runewidth.FillRight(cat.glyph, 2)))

	// name with highlighted matches
	pos := MatchPositions(ix, e, m.q)
	nameSt := bg.Foreground(cat.color)
	if e.IsDir() {
		nameSt = nameSt.Bold(true)
	}
	b.WriteString(highlightRuns(name, int(e.Base), pos, nameSt, bg.Inherit(sMatch)))

	right := humanAge(time.Unix(e.Mod, 0))
	if !e.IsDir() {
		right = fmt.Sprintf("%8s  %7s", humanSize(e.Size), right)
	}
	right += " "
	used := lipgloss.Width(b.String())
	avail := w - used - lipgloss.Width(right) - 3
	dir := strings.TrimRight(path[:e.Base], `\/`)
	if avail > 4 {
		b.WriteString(bg.Render("  "))
		b.WriteString(highlightRuns(truncLeft(dir, avail), -1, nil, bg.Inherit(sDim), bg))
	}
	return finishRow(b.String(), bg.Inherit(sFaint).Render(right), w, bg)
}

func (m *model) contentRow(i, w int) string {
	h := m.chits[i]
	e := &m.chitsIx.Entries[h.Idx]
	sel := i == m.cursor
	bg := rowStyle(sel)
	name := m.chitsIx.Name(e)
	cat := categoryOf(name, false)

	var b strings.Builder
	if sel {
		b.WriteString(bg.Foreground(cAccent).Render("▌"))
	} else {
		b.WriteString(" ")
	}
	sameFile := i > 0 && m.chits[i-1].Idx == h.Idx
	label := name
	if sameFile {
		label = strings.Repeat(" ", min(runewidth.StringWidth(name), 24))
		if runewidth.StringWidth(name) > 24 {
			label = runewidth.Truncate(name, 24, "…")
			label = strings.Repeat(" ", runewidth.StringWidth(label))
		}
	} else {
		label = runewidth.Truncate(name, 24, "…")
	}
	b.WriteString(bg.Foreground(cat.color).Render(label))
	b.WriteString(bg.Inherit(sFaint).Render(fmt.Sprintf(":%-5d ", h.Line)))

	text := h.Text
	col := min(h.Col, len(text))
	end := min(col+h.MLen, len(text))
	b.WriteString(bg.Foreground(cText).Render(text[:col]))
	b.WriteString(bg.Inherit(sMatch).Render(text[col:end]))
	b.WriteString(bg.Foreground(cText).Render(text[end:]))
	return finishRow(b.String(), "", w, bg)
}

// highlightRuns renders s, using hl for byte offsets (offset+i) in pos.
func highlightRuns(s string, offset int, pos map[int]bool, normal, hl lipgloss.Style) string {
	if len(pos) == 0 || offset < 0 {
		return normal.Render(s)
	}
	var b, run strings.Builder
	cur := false
	flush := func() {
		if run.Len() == 0 {
			return
		}
		if cur {
			b.WriteString(hl.Render(run.String()))
		} else {
			b.WriteString(normal.Render(run.String()))
		}
		run.Reset()
	}
	for i, r := range s {
		on := pos[offset+i]
		if on != cur {
			flush()
			cur = on
		}
		run.WriteRune(r)
	}
	flush()
	return b.String()
}

func finishRow(left, right string, w int, bg lipgloss.Style) string {
	rw := lipgloss.Width(right)
	left = ansi.Truncate(left, w-rw, "…")
	gap := w - lipgloss.Width(left) - rw
	if gap < 0 {
		gap = 0
	}
	return left + bg.Render(strings.Repeat(" ", gap)) + right
}

func (m *model) previewView(w int) string {
	h := m.mainHeight()
	inner := w - 2
	e, _ := m.selected()
	var lines []string
	title, info := "Preview", ""
	if e != nil {
		path := m.listIx().Path(e)
		title = truncLeft(path, max(10, inner-20))
		meta := humanAge(time.Unix(e.Mod, 0)) + " · " + time.Unix(e.Mod, 0).Format("2006-01-02 15:04")
		if !e.IsDir() {
			meta = humanSize(e.Size) + " · " + meta
		}
		lines = append(lines, " "+sDim.Render(meta), " "+sFaint.Render(strings.Repeat("─", max(0, inner-2))))
	}
	body := h - 2 - len(lines)
	switch {
	case e == nil:
	case m.pv.key == "":
		lines = append(lines, " "+sFaint.Render("loading…"))
	case len(m.pv.lines) == 0:
		lines = append(lines, centerMsg(body, inner, sDim.Render(m.pv.note))...)
	default:
		gutter := 0
		if m.pv.firstLine > 0 {
			gutter = len(fmt.Sprint(m.pv.firstLine + len(m.pv.lines)))
		}
		scroll := min(m.pvScroll, max(0, len(m.pv.lines)-1))
		for i := scroll; i < min(len(m.pv.lines), scroll+body); i++ {
			var g string
			if gutter > 0 {
				num := fmt.Sprintf("%*d ", gutter, m.pv.firstLine+i)
				if i == m.pv.focus {
					g = lipgloss.NewStyle().Foreground(cMatch).Bold(true).Render("▶" + num)
				} else {
					g = sFaint.Render(" " + num)
				}
			}
			l := g + m.pv.lines[i]
			if i == m.pv.focus {
				l = ansi.Truncate(l, inner, "…")
				l += lipgloss.NewStyle().Background(cSelBg).Render(strings.Repeat(" ", max(0, inner-lipgloss.Width(l))))
			}
			lines = append(lines, l)
		}
		if len(m.pv.lines) > body {
			info = fmt.Sprintf("%d%%", min(100, (scroll+body)*100/len(m.pv.lines)))
		}
	}
	return drawBox(title, info, lines, w, h, false)
}

func (m *model) footerView() string {
	var left string
	if m.flash != "" {
		st := sOK
		if m.flashErr {
			st = sErr
		}
		left = st.Render(" " + m.flash)
	} else if m.mode == modeName {
		if m.ix != nil {
			s := fmt.Sprintf(" %s matches", commas(m.total))
			if m.total > len(m.hits) {
				s += fmt.Sprintf(" (top %s)", commas(len(m.hits)))
			}
			left = sDim.Render(s+" · ") + sKey.Render(fmtDur(m.took)) + sDim.Render(" · sort "+m.effectiveSort().String())
		}
	} else if m.cScanned != nil {
		state := sDim.Render("done in ") + sKey.Render(fmtDur(m.cTook))
		if m.cRunning {
			state = sKey.Render(spinner(m.frame)) + sDim.Render(" "+fmtDur(time.Since(m.cStart)))
		}
		left = sDim.Render(fmt.Sprintf(" %s hits · %s files scanned · ", commas(len(m.chits)), commas(int(m.cScanned.Load())))) + state
	}
	keys := []string{"enter", "open", "^o", "reveal", "^y", "copy", "tab", "mode", "^s", "sort", "^t", "preview", "^r", "reindex", "F1", "help"}
	right := ""
	for i := 0; i < len(keys); i += 2 {
		next := right + sKey.Render(keys[i]) + " " + sFaint.Render(keys[i+1]) + "  "
		if lipgloss.Width(left)+lipgloss.Width(next)+2 > m.w {
			break // drop whole hints rather than cutting one in half
		}
		right = next
	}
	return padBetween(left, right, m.w)
}

func (m *model) effectiveSort() SortMode {
	if m.sort == SortRelevance && m.q != nil && !m.q.HasTerms() {
		return SortNewest
	}
	return m.sort
}

func (m model) helpView() string {
	k := func(key, desc string) string {
		return "  " + sKey.Render(fmt.Sprintf("%-14s", key)) + sDim.Render(desc)
	}
	sec := func(s string) string { return "\n " + sPaneTitle.Render(s) }
	lines := []string{
		sec("Search syntax"),
		k("foo bar", "fuzzy match all terms against the path (basename ranks higher)"),
		k("'foo", "exact substring          ^foo  name starts with"),
		k("foo$", "path ends with           !foo  exclude paths containing foo"),
		k("ext:go,rs", "file extension(s)"),
		k("is:dir / is:file", "only folders / only files"),
		k("size:>10mb", "size filter: >, <, or 1mb..1gb (b, kb, mb, gb, tb)"),
		k("mod:<7d", "modified within 7 days; mod:>1y older than a year (min,h,d,w,mo,y)"),
		k("in:projects", "folder name in path; in:E:\\work for a path prefix"),
		sec("Keys"),
		k("↑ ↓ / ^p ^n", "move           pgup pgdn  page"),
		k("enter", "open with default app (click a selected row too)"),
		k("ctrl+o", "reveal in Explorer"),
		k("ctrl+y", "copy path to clipboard"),
		k("tab", "switch between name search and content search"),
		k("ctrl+x", "toggle regex for content search"),
		k("ctrl+s", "cycle sort: relevance → newest → largest"),
		k("ctrl+t", "toggle preview      shift+↑↓ / wheel  scroll preview"),
		k("ctrl+r", "rebuild index"),
		k("esc", "clear query, then quit"),
		"",
		"  " + sFaint.Render("index: "+indexPath()),
		"",
		"  " + sFaint.Render("press any key to close"),
	}
	return drawBox("seek · help", "", strings.Split(strings.Join(lines, "\n"), "\n"), m.w, m.h, true)
}

// ---------- drawing helpers ----------

func drawBox(title, info string, lines []string, w, h int, focus bool) string {
	bc := cBorder
	if focus {
		bc = cAccent
	}
	bs := lipgloss.NewStyle().Foreground(bc)
	inner := w - 2
	top := "─"
	if title != "" {
		t := " " + title + " "
		t = ansi.Truncate(t, max(0, inner-2-lipgloss.Width(info)-3), "…")
		top += sPaneTitle.Render(t)
	}
	used := 1 + lipgloss.Width(top) - 1
	infoS := ""
	if info != "" {
		infoS = sDim.Render(" " + info + " ")
	}
	fill := inner - used - lipgloss.Width(infoS) - 1
	var b strings.Builder
	b.WriteString(bs.Render("╭─"))
	b.WriteString(strings.TrimPrefix(top, "─"))
	b.WriteString(bs.Render(strings.Repeat("─", max(0, fill))))
	b.WriteString(infoS)
	b.WriteString(bs.Render("─╮"))
	b.WriteByte('\n')
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = ansi.Truncate(lines[i], inner, "…")
		}
		pad := max(0, inner-lipgloss.Width(l))
		b.WriteString(bs.Render("│"))
		b.WriteString(l)
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(bs.Render("│"))
		b.WriteByte('\n')
	}
	b.WriteString(bs.Render("╰" + strings.Repeat("─", max(0, inner)) + "╯"))
	return b.String()
}

func padBetween(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return ansi.Truncate(left+" "+right, w, "")
	}
	return left + strings.Repeat(" ", gap) + right
}

func centerMsg(h, w int, msgs ...string) []string {
	lines := make([]string, max(0, h/2-len(msgs)/2))
	for _, s := range msgs {
		pad := max(0, (w-lipgloss.Width(s))/2)
		lines = append(lines, strings.Repeat(" ", pad)+s)
	}
	return lines
}

func truncLeft(s string, w int) string {
	if runewidth.StringWidth(s) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	rs := []rune(s)
	width := 0
	i := len(rs)
	for i > 0 {
		cw := runewidth.RuneWidth(rs[i-1])
		if width+cw > w-1 {
			break
		}
		width += cw
		i--
	}
	return "…" + string(rs[i:])
}

func spinner(frame int) string {
	f := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return f[frame%len(f)]
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}

func runTUI(ix *Index, initial string, mode uiMode, pick bool) error {
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithMouseCellMotion()}
	if pick {
		opts = append(opts, tea.WithOutput(os.Stderr))
	}
	roots, excludes := defaultRoots(), defaultExcludes
	if ix != nil {
		roots, excludes = ix.Roots, ix.Excludes
	}
	upd := NewUpdater(ix, roots, excludes)
	prog = tea.NewProgram(newModel(ix, upd, initial, mode, pick), opts...)
	upd.OnUpdate = func(ix *Index, n int, full bool) { prog.Send(indexUpdatedMsg{ix: ix, changes: n, full: full}) }
	upd.OnRebuildStart = func(p *atomic.Int64) { prog.Send(indexStartMsg{progress: p}) }
	upd.OnError = func(err error) { prog.Send(indexErrMsg{err}) }
	upd.Start()
	final, err := prog.Run()
	upd.Close() // saves any unsaved live changes
	if err != nil {
		return err
	}
	if fm, ok := final.(model); ok && fm.picked != "" {
		fmt.Println(fm.picked)
	}
	return nil
}
