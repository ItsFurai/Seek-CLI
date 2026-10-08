package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	cAccent  = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#A78BFA"}
	cAccent2 = lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#67E8F9"}
	cMatch   = lipgloss.AdaptiveColor{Light: "#C2410C", Dark: "#FDBA74"}
	cDim     = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#6B7280"}
	cFaint   = lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#4B5563"}
	cText    = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E5E7EB"}
	cSelBg   = lipgloss.AdaptiveColor{Light: "#EDE9FE", Dark: "#2E2A47"}
	cBorder  = lipgloss.AdaptiveColor{Light: "#D1D5DB", Dark: "#3F3F55"}
	cErr     = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	cOK      = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#86EFAC"}

	sLogo      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1E1B2E")).Background(cAccent).Padding(0, 1)
	sTabOn     = lipgloss.NewStyle().Bold(true).Foreground(cAccent).Underline(true)
	sTabOff    = lipgloss.NewStyle().Foreground(cDim)
	sDim       = lipgloss.NewStyle().Foreground(cDim)
	sFaint     = lipgloss.NewStyle().Foreground(cFaint)
	sMatch     = lipgloss.NewStyle().Foreground(cMatch).Bold(true)
	sKey       = lipgloss.NewStyle().Foreground(cAccent2).Bold(true)
	sErr       = lipgloss.NewStyle().Foreground(cErr)
	sOK        = lipgloss.NewStyle().Foreground(cOK)
	sPaneTitle = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	sBox       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cBorder)
	sBoxFocus  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent)
)

type category struct {
	color lipgloss.AdaptiveColor
	glyph string
}

var (
	catDir     = category{lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#60A5FA"}, "▸"}
	catCode    = category{lipgloss.AdaptiveColor{Light: "#047857", Dark: "#6EE7B7"}, "‹›"}
	catDoc     = category{lipgloss.AdaptiveColor{Light: "#A16207", Dark: "#FDE68A"}, "≡"}
	catImage   = category{lipgloss.AdaptiveColor{Light: "#BE185D", Dark: "#F9A8D4"}, "◩"}
	catMedia   = category{lipgloss.AdaptiveColor{Light: "#7E22CE", Dark: "#D8B4FE"}, "♪"}
	catArchive = category{lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FCA5A5"}, "▣"}
	catExec    = category{lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FCA5A5"}, "⚙"}
	catOther   = category{lipgloss.AdaptiveColor{Light: "#374151", Dark: "#D1D5DB"}, "·"}
)

var extCat = map[string]category{}

func init() {
	add := func(c category, exts string) {
		for _, e := range strings.Fields(exts) {
			extCat[e] = c
		}
	}
	kindCat := map[string]category{"code": catCode, "doc": catDoc, "image": catImage,
		"audio": catMedia, "video": catMedia, "archive": catArchive, "app": catExec}
	for kind, exts := range kindExts {
		add(kindCat[kind], exts)
	}
}

func categoryOf(name string, isDir bool) category {
	if isDir {
		return catDir
	}
	if c, ok := extCat[extOf(name)]; ok {
		return c
	}
	return catOther
}

func humanSize(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	f, i := float64(n), 0
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	for f /= u; f >= u && i < len(units)-1; i++ {
		f /= u
	}
	if f < 10 {
		return fmt.Sprintf("%.1f %s", f, units[i])
	}
	return fmt.Sprintf("%.0f %s", f, units[i])
}

func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 0:
		return "future"
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/24/30))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
	}
}

func commas(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + commas(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
