package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
)

type previewData struct {
	key       string
	lines     []string // already styled
	firstLine int      // line number of lines[0] (1-based); 0 = no gutter
	focus     int      // index into lines to highlight, -1 = none
	note      string
}

const previewReadLimit = 4 << 20

func previewKey(path string, line int) string { return fmt.Sprintf("%s:%d", path, line) }

// previewSource maps a displayed path to the file to read; tests swap it to show demo data.
var previewSource = func(path string) string { return path }

func loadPreview(path string, isDir bool, focusLine int) previewData {
	pd := previewData{key: previewKey(path, focusLine), focus: -1}
	path = previewSource(path)
	if isDir {
		return previewDir(path, pd)
	}
	f, err := os.Open(path)
	if err != nil {
		pd.note = err.Error()
		return pd
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, previewReadLimit))
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 || !utf8.Valid(data[:min(len(data), 4000)]) && !looksText(data) {
		pd.note = "binary file — press enter to open it"
		return pd
	}
	if len(data) == 0 {
		pd.note = "empty file"
		return pd
	}

	all := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	lo, hi := 0, min(len(all), 400)
	if focusLine > 0 {
		lo = max(0, focusLine-1-150)
		hi = min(len(all), focusLine-1+250)
		pd.focus = focusLine - 1 - lo
	}
	window := strings.ReplaceAll(strings.Join(all[lo:hi], "\n"), "\t", "    ")
	pd.firstLine = lo + 1
	pd.lines = highlight(path, window)
	return pd
}

func looksText(b []byte) bool {
	b = b[:min(len(b), 4000)]
	bad := 0
	for _, c := range b {
		if c < 9 || c > 13 && c < 32 {
			bad++
		}
	}
	return bad*20 < len(b)
}

var hlStyle *chroma.Style

func highlight(path, text string) []string {
	lexer := lexers.Match(filepath.Base(path))
	if lexer == nil {
		lexer = lexers.Analyse(text)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	if hlStyle == nil {
		name := "catppuccin-mocha"
		if !lipgloss.HasDarkBackground() {
			name = "catppuccin-latte"
		}
		if hlStyle = styles.Get(name); hlStyle == nil {
			hlStyle = styles.Fallback
		}
	}
	it, err := lexer.Tokenise(nil, text)
	if err != nil {
		return strings.Split(text, "\n")
	}
	cache := map[chroma.TokenType]lipgloss.Style{}
	var out []string
	for _, toks := range chroma.SplitTokensIntoLines(it.Tokens()) {
		var sb strings.Builder
		for _, t := range toks {
			v := strings.TrimRight(t.Value, "\n")
			if v == "" {
				continue
			}
			st, ok := cache[t.Type]
			if !ok {
				e := hlStyle.Get(t.Type)
				st = lipgloss.NewStyle()
				if e.Colour.IsSet() {
					st = st.Foreground(lipgloss.Color(e.Colour.String()))
				}
				if e.Bold == chroma.Yes {
					st = st.Bold(true)
				}
				if e.Italic == chroma.Yes {
					st = st.Italic(true)
				}
				cache[t.Type] = st
			}
			sb.WriteString(st.Render(v))
		}
		out = append(out, sb.String())
	}
	return out
}

func previewDir(path string, pd previewData) previewData {
	ents, err := os.ReadDir(path)
	if err != nil {
		pd.note = err.Error()
		return pd
	}
	sort.Slice(ents, func(i, j int) bool {
		if ents[i].IsDir() != ents[j].IsDir() {
			return ents[i].IsDir()
		}
		return strings.ToLower(ents[i].Name()) < strings.ToLower(ents[j].Name())
	})
	nd := 0
	for _, e := range ents {
		if e.IsDir() {
			nd++
		}
	}
	pd.lines = append(pd.lines, sDim.Render(fmt.Sprintf("%d folders · %d files", nd, len(ents)-nd)), "")
	for i, e := range ents {
		if i >= 500 {
			pd.lines = append(pd.lines, sDim.Render(fmt.Sprintf("… %d more", len(ents)-i)))
			break
		}
		c := categoryOf(e.Name(), e.IsDir())
		name := e.Name()
		if e.IsDir() {
			name += string(os.PathSeparator)
		}
		line := lipgloss.NewStyle().Foreground(c.color).Render(c.glyph + " " + name)
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				line += "  " + sFaint.Render(humanSize(info.Size()))
			}
		}
		pd.lines = append(pd.lines, line)
	}
	if len(ents) == 0 {
		pd.note = "empty folder"
	}
	return pd
}
