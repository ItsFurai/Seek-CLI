package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type termKind int

const (
	termFuzzy  termKind = iota
	termExact           // 'foo   substring
	termPrefix          // ^foo   basename starts with
	termSuffix          // foo$   path ends with
	termNot             // !foo   must not contain
)

type term struct {
	kind termKind
	text []byte // lowercased
}

// Query is a parsed search line: free-text terms plus key:value filters.
type Query struct {
	Terms    []term
	Raw      []string // free-text terms in original case (used by content search)
	Exts     map[string]bool
	OnlyDirs bool
	OnlyFile bool
	MinSize  int64
	MaxSize  int64 // -1 = unbounded
	ModAfter int64 // unix; 0 = unbounded
	ModBefor int64
	In       []string // lowercased directory constraints
	Err      string
}

func (q *Query) HasTerms() bool   { return len(q.Terms) > 0 }
func (q *Query) Text() string     { return strings.Join(q.Raw, " ") }
func (q *Query) HasFilters() bool { return len(q.Exts) > 0 || q.OnlyDirs || q.OnlyFile || q.MinSize > 0 || q.MaxSize >= 0 || q.ModAfter != 0 || q.ModBefor != 0 || len(q.In) > 0 }

func normSep(s string) string {
	if os.PathSeparator == '\\' {
		return strings.ReplaceAll(s, "/", `\`)
	}
	return s
}

func ParseQuery(s string, now time.Time) *Query {
	q := &Query{MaxSize: -1}
	for _, tok := range strings.Fields(s) {
		if k, v, ok := strings.Cut(tok, ":"); ok && v != "" && isFilterKey(k) {
			if err := q.applyFilter(strings.ToLower(k), v, now); err != nil {
				q.Err = err.Error()
			}
			continue
		}
		q.Raw = append(q.Raw, tok)
		t := term{kind: termFuzzy}
		low := strings.ToLower(normSep(tok))
		switch {
		case strings.HasPrefix(low, "!") && len(low) > 1:
			t.kind, low = termNot, low[1:]
		case strings.HasPrefix(low, "'") && len(low) > 1:
			t.kind, low = termExact, low[1:]
		case strings.HasPrefix(low, "^") && len(low) > 1:
			t.kind, low = termPrefix, low[1:]
		case strings.HasSuffix(low, "$") && len(low) > 1:
			t.kind, low = termSuffix, low[:len(low)-1]
		}
		t.text = []byte(low)
		q.Terms = append(q.Terms, t)
	}
	return q
}

func isFilterKey(k string) bool {
	switch strings.ToLower(k) {
	case "ext", "e", "is", "type", "size", "sz", "mod", "modified", "in", "dir":
		return true
	}
	return false
}

func (q *Query) applyFilter(k, v string, now time.Time) error {
	switch k {
	case "ext", "e":
		if q.Exts == nil {
			q.Exts = map[string]bool{}
		}
		for _, e := range strings.Split(strings.ToLower(v), ",") {
			if e = strings.TrimPrefix(e, "."); e != "" {
				q.Exts[e] = true
			}
		}
		q.OnlyFile = true
	case "is", "type":
		switch strings.ToLower(v) {
		case "d", "dir", "folder", "directory":
			q.OnlyDirs = true
		case "f", "file":
			q.OnlyFile = true
		default:
			return fmt.Errorf("is: expects file or dir")
		}
	case "size", "sz":
		lo, hi, err := parseRange(v, parseSize)
		if err != nil {
			return fmt.Errorf("size: %v (try size:>10mb)", err)
		}
		q.MinSize, q.MaxSize = lo, hi
		q.OnlyFile = true
	case "mod", "modified":
		// mod:<7d (or mod:7d) = changed within the last 7 days; mod:>1y = older than a year
		if !strings.ContainsAny(v[:1], "<>") && !strings.Contains(v, "..") {
			v = "<" + v
		}
		lo, hi, err := parseRange(v, parseAge)
		if err != nil {
			return fmt.Errorf("mod: %v (try mod:<7d)", err)
		}
		if lo > 0 {
			q.ModBefor = now.Unix() - lo
		}
		if hi >= 0 {
			q.ModAfter = now.Unix() - hi
		}
	case "in", "dir":
		v = normSep(v)
		if abs, err := filepath.Abs(v); err == nil && (filepath.IsAbs(v) || strings.HasPrefix(v, ".")) {
			v = abs
		}
		q.In = append(q.In, strings.ToLower(v))
	}
	return nil
}

// parseRange handles ">x", ">=x", "<x", "<=x", "a..b" and bare "x" (treated as >=x).
func parseRange(v string, unit func(string) (int64, error)) (lo, hi int64, err error) {
	lo, hi = 0, -1
	switch {
	case strings.Contains(v, ".."):
		a, b, _ := strings.Cut(v, "..")
		if lo, err = unit(a); err != nil {
			return
		}
		hi, err = unit(b)
	case strings.HasPrefix(v, ">"):
		lo, err = unit(strings.TrimLeft(v, ">="))
	case strings.HasPrefix(v, "<"):
		hi, err = unit(strings.TrimLeft(v, "<="))
	default:
		lo, err = unit(v)
	}
	return
}

func splitNum(s string) (float64, string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	if i == 0 {
		return 0, "", fmt.Errorf("bad number %q", s)
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	return n, s[i:], err
}

func parseSize(s string) (int64, error) {
	n, u, err := splitNum(s)
	if err != nil {
		return 0, err
	}
	mult := map[string]float64{"": 1, "b": 1, "k": 1 << 10, "kb": 1 << 10, "m": 1 << 20, "mb": 1 << 20, "g": 1 << 30, "gb": 1 << 30, "t": 1 << 40, "tb": 1 << 40}[u]
	if mult == 0 {
		return 0, fmt.Errorf("unknown unit %q", u)
	}
	return int64(n * mult), nil
}

func parseAge(s string) (int64, error) {
	n, u, err := splitNum(s)
	if err != nil {
		return 0, err
	}
	mult := map[string]float64{"s": 1, "min": 60, "h": 3600, "": 86400, "d": 86400, "w": 7 * 86400, "mo": 30 * 86400, "y": 365 * 86400}[u]
	if mult == 0 {
		return 0, fmt.Errorf("unknown unit %q (use min,h,d,w,mo,y)", u)
	}
	return int64(n * mult), nil
}

func extOf(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[i+1:])
}

// passFilters checks the cheap metadata filters.
func (q *Query) passFilters(ix *Index, e *Entry) bool {
	if q.OnlyDirs && !e.IsDir() || q.OnlyFile && e.IsDir() {
		return false
	}
	if e.Size < q.MinSize || q.MaxSize >= 0 && e.Size > q.MaxSize {
		return false
	}
	if q.ModAfter != 0 && e.Mod < q.ModAfter || q.ModBefor != 0 && e.Mod > q.ModBefor {
		return false
	}
	if len(q.Exts) > 0 && !q.Exts[extOf(ix.Name(e))] {
		return false
	}
	if len(q.In) > 0 {
		low := ix.Lower(e)
		dir := low[:e.Base]
		for _, in := range q.In {
			if filepath.IsAbs(in) {
				if !hasPrefix(low, in) {
					return false
				}
			} else if !contains(dir, in) {
				return false
			}
		}
	}
	return true
}

func hasPrefix(b []byte, s string) bool { return len(b) >= len(s) && string(b[:len(s)]) == s }
func contains(b []byte, s string) bool  { return strings.Contains(unsafeStr(b), s) }
