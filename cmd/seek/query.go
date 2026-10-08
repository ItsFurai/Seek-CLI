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
	termName            // foo/   fuzzy, but only against the name (not the folder path)
)

type term struct {
	kind termKind
	text []byte // lowercased
}

// Query is a parsed search line: free-text terms plus filters, written either
// as key:value (ext:pdf) or as shorthands (.pdf >10mb today photos/ E:\work).
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
	Labels   []string // plain-English reading of each token, for the UI
}

func (q *Query) HasTerms() bool { return len(q.Terms) > 0 }
func (q *Query) Text() string   { return strings.Join(q.Raw, " ") }
func (q *Query) HasFilters() bool {
	return len(q.Exts) > 0 || q.OnlyDirs || q.OnlyFile || q.MinSize > 0 || q.MaxSize >= 0 || q.ModAfter != 0 || q.ModBefor != 0 || len(q.In) > 0
}

// Describe is the query in words, e.g. `"report" · PDF files · over 10 MB · changed this week`.
func (q *Query) Describe() string { return strings.Join(q.Labels, " · ") }

func normSep(s string) string {
	if os.PathSeparator == '\\' {
		return strings.ReplaceAll(s, "/", `\`)
	}
	return s
}

// File kinds, shared with the result colors in style.go.
var kindExts = map[string]string{
	"code": `go rs c h cpp hpp cc cs java kt kts scala py rb php js mjs cjs ts tsx jsx vue svelte swift m mm
		lua pl sh bash zsh ps1 psm1 bat cmd sql r dart zig nim ex exs erl hs ml fs clj json yaml yml toml xml
		html htm css scss sass less ini cfg conf gradle cmake mk makefile dockerfile proto graphql tf ipynb`,
	"doc":     `txt md markdown rst org pdf doc docx odt rtf xls xlsx csv tsv ods ppt pptx odp epub log tex pages numbers key`,
	"image":   `png jpg jpeg gif bmp ico svg webp tif tiff heic heif psd raw avif cr2 nef arw dng`,
	"audio":   `mp3 wav flac ogg opus m4a aac wma aiff mid midi`,
	"video":   `mp4 mkv avi mov wmv webm flv m4v mpg mpeg 3gp ts`,
	"archive": `zip 7z rar gz tgz bz2 xz zst tar iso img dmg cab jar`,
	"app":     `exe msi dll sys com lnk appx msix apk app deb rpm`,
}

var kindAliases = map[string]string{
	"code": "code", "source": "code", "src": "code",
	"doc": "doc", "docs": "doc", "document": "doc", "documents": "doc", "text": "doc",
	"image": "image", "images": "image", "img": "image", "photo": "image", "photos": "image",
	"picture": "image", "pictures": "image", "pic": "image", "pics": "image",
	"audio": "audio", "music": "audio", "song": "audio", "songs": "audio", "sound": "audio",
	"video": "video", "videos": "video", "vid": "video", "movie": "video", "movies": "video",
	"archive": "archive", "archives": "archive", "zip": "archive", "compressed": "archive",
	"app": "app", "apps": "app", "program": "app", "programs": "app", "exe": "app", "executable": "app",
}

var kindLabels = map[string]string{
	"code": "code files", "doc": "documents", "image": "images", "audio": "audio files",
	"video": "videos", "archive": "archives", "app": "programs",
}

const sizeUnits = "b k kb m mb g gb t tb"
const ageUnits = "s min h d w mo y"

func ParseQuery(s string, now time.Time) *Query {
	q := &Query{MaxSize: -1}
	for _, tok := range strings.Fields(s) {
		// before key:value, so E:\work isn't read as the e: (ext) filter
		if isAbsPath(tok) {
			if err := q.applyFilter("in", tok, now); err != nil {
				q.Err = err.Error()
			}
			continue
		}
		if k, v, ok := strings.Cut(tok, ":"); ok && v != "" && isFilterKey(k) {
			if err := q.applyFilter(strings.ToLower(k), v, now); err != nil {
				q.Err = err.Error()
			}
			continue
		}
		if q.shorthand(tok, now) {
			continue
		}
		t := term{kind: termFuzzy}
		low := strings.ToLower(normSep(tok))
		raw := tok
		switch {
		case strings.HasPrefix(low, "!") && len(low) > 1:
			t.kind, low = termNot, low[1:]
			q.Labels = append(q.Labels, fmt.Sprintf("without %q", tok[1:]))
		case strings.HasPrefix(low, "'") && len(low) > 1:
			t.kind, low, raw = termExact, low[1:], tok[1:]
			q.Labels = append(q.Labels, fmt.Sprintf("exactly %q", raw))
		case strings.HasPrefix(low, "^") && len(low) > 1:
			t.kind, low = termPrefix, low[1:]
			q.Labels = append(q.Labels, fmt.Sprintf("name starts with %q", tok[1:]))
		case strings.HasSuffix(low, "$") && len(low) > 1:
			t.kind, low = termSuffix, low[:len(low)-1]
			q.Labels = append(q.Labels, fmt.Sprintf("ends with %q", tok[:len(tok)-1]))
		default:
			q.Labels = append(q.Labels, fmt.Sprintf("%q", tok))
		}
		q.Raw = append(q.Raw, raw)
		t.text = []byte(low)
		q.Terms = append(q.Terms, t)
	}
	return q
}

// shorthand handles filters that need no key: .pdf  >10mb  <7d  today  photos/  E:\work
func (q *Query) shorthand(tok string, now time.Time) bool {
	low := strings.ToLower(tok)
	switch {
	case isExtList(low): // .pdf or .jpg,.png
		q.addExts(strings.Split(low, ","))
		return true

	case strings.HasPrefix(low, ">") || strings.HasPrefix(low, "<") || strings.Contains(low, ".."):
		v := strings.TrimLeft(low, "<>=")
		if a, _, ok := strings.Cut(low, ".."); ok {
			v = a // the unit on one side decides size vs. date; parseRange checks both
		}
		switch unitOf(v) {
		case "size":
			return q.applyFilter("size", low, now) == nil || q.fail("size", low)
		case "age":
			return q.applyFilter("mod", low, now) == nil || q.fail("date", low)
		}

	case strings.HasSuffix(tok, "/") || strings.HasSuffix(tok, `\`):
		name := strings.TrimRight(tok, `/\`)
		if name == "" || isAbsPath(tok) {
			break
		}
		q.OnlyDirs = true
		q.Raw = append(q.Raw, name)
		q.Terms = append(q.Terms, term{kind: termName, text: []byte(strings.ToLower(normSep(name)))})
		q.Labels = append(q.Labels, fmt.Sprintf("folders like %q", name))
		return true

	}

	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	switch low {
	case "today":
		q.ModAfter = midnight
	case "yesterday":
		q.ModAfter, q.ModBefor = midnight-86400, midnight
	case "week":
		q.ModAfter = now.Unix() - 7*86400
	case "month":
		q.ModAfter = now.Unix() - 30*86400
	case "year":
		q.ModAfter = now.Unix() - 365*86400
	default:
		return false
	}
	q.Labels = append(q.Labels, map[string]string{"today": "changed today", "yesterday": "changed yesterday",
		"week": "changed this week", "month": "changed this month", "year": "changed this year"}[low])
	return true
}

func (q *Query) fail(what, v string) bool {
	q.Err = fmt.Sprintf("can't read %s %q (try >10mb or <7d)", what, v)
	return true
}

func isExtList(s string) bool {
	for _, part := range strings.Split(s, ",") {
		if len(part) < 2 || part[0] != '.' || len(part) > 16 {
			return false
		}
		letter := false
		for _, c := range part[1:] {
			switch {
			case c >= 'a' && c <= 'z':
				letter = true
			case c >= '0' && c <= '9', c == '_', c == '-':
			default:
				return false
			}
		}
		if !letter {
			return false
		}
	}
	return true
}

// unitOf says whether "10mb" is a size, "7d" an age, or neither. A unit is required.
func unitOf(v string) string {
	_, u, err := splitNum(v)
	if err != nil || u == "" {
		return ""
	}
	for _, s := range strings.Fields(sizeUnits) {
		if u == s {
			return "size"
		}
	}
	for _, a := range strings.Fields(ageUnits) {
		if u == a {
			return "age"
		}
	}
	return ""
}

func isAbsPath(s string) bool {
	if strings.HasPrefix(s, "~") {
		return true
	}
	return filepath.IsAbs(normSep(s))
}

func (q *Query) addExts(exts []string) {
	if q.Exts == nil {
		q.Exts = map[string]bool{}
	}
	var names []string
	for _, e := range exts {
		if e = strings.TrimPrefix(strings.ToLower(e), "."); e != "" {
			q.Exts[e] = true
			names = append(names, strings.ToUpper(e))
		}
	}
	q.OnlyFile = true
	if len(names) == 1 {
		q.Labels = append(q.Labels, names[0]+" files")
	} else if len(names) > 1 {
		q.Labels = append(q.Labels, strings.Join(names, "/")+" files")
	}
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
		q.addExts(strings.Split(v, ","))
	case "is", "type":
		lv := strings.ToLower(v)
		switch lv {
		case "d", "dir", "dirs", "folder", "folders", "directory":
			q.OnlyDirs = true
			q.Labels = append(q.Labels, "folders only")
		case "f", "file", "files":
			q.OnlyFile = true
			q.Labels = append(q.Labels, "files only")
		default:
			kind, ok := kindAliases[lv]
			if !ok {
				return fmt.Errorf("is: expects file, dir, image, video, audio, doc, code, archive or app")
			}
			n := len(q.Labels)
			q.addExts(strings.Fields(kindExts[kind]))
			q.Labels = append(q.Labels[:n], kindLabels[kind])
		}
	case "size", "sz":
		lo, hi, err := parseRange(v, parseSize)
		if err != nil {
			return fmt.Errorf("size: %v (try >10mb)", err)
		}
		q.MinSize, q.MaxSize = lo, hi
		q.OnlyFile = true
		switch {
		case hi < 0:
			q.Labels = append(q.Labels, "over "+humanSize(lo))
		case lo == 0:
			q.Labels = append(q.Labels, "under "+humanSize(hi))
		default:
			q.Labels = append(q.Labels, humanSize(lo)+" to "+humanSize(hi))
		}
	case "mod", "modified":
		// mod:<7d (or mod:7d) = changed within the last 7 days; mod:>1y = older than a year
		if !strings.ContainsAny(v[:1], "<>") && !strings.Contains(v, "..") {
			v = "<" + v
		}
		lo, hi, err := parseRange(v, parseAge)
		if err != nil {
			return fmt.Errorf("mod: %v (try <7d)", err)
		}
		if lo > 0 {
			q.ModBefor = now.Unix() - lo
		}
		if hi >= 0 {
			q.ModAfter = now.Unix() - hi
		}
		// describe in the units the user typed: <7d is "7 days", not "1 week"
		a, b, isRange := strings.Cut(strings.TrimLeft(v, "<>="), "..")
		switch {
		case isRange:
			q.Labels = append(q.Labels, "changed "+ageText(a)+" to "+ageText(b)+" ago")
		case hi < 0:
			q.Labels = append(q.Labels, "not changed for "+ageText(a))
		default:
			q.Labels = append(q.Labels, "changed in the last "+ageText(a))
		}
	case "in", "dir":
		v = normSep(v)
		if strings.HasPrefix(v, "~") {
			if h, err := os.UserHomeDir(); err == nil {
				v = h + v[1:]
			}
		}
		if abs, err := filepath.Abs(v); err == nil && (filepath.IsAbs(v) || strings.HasPrefix(v, ".")) {
			v = abs
		}
		q.In = append(q.In, strings.ToLower(strings.TrimRight(v, `\/`)))
		q.Labels = append(q.Labels, "in "+v)
	}
	return nil
}

// ageText turns "7d" into "7 days" and "1y" into "1 year".
func ageText(s string) string {
	n, u, err := splitNum(s)
	if err != nil {
		return s
	}
	word := map[string]string{"s": "second", "min": "minute", "h": "hour", "": "day", "d": "day",
		"w": "week", "mo": "month", "y": "year"}[u]
	num := strconv.FormatFloat(n, 'f', -1, 64)
	if n == 1 {
		return num + " " + word
	}
	return num + " " + word + "s"
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
	if len(q.Exts) > 0 {
		name := ix.Name(e)
		if !q.Exts[extOf(name)] && !(len(name) > 1 && name[0] == '.' && q.Exts[strings.ToLower(name[1:])]) {
			return false
		}
	}
	if len(q.In) > 0 {
		low := ix.Lower(e)
		dir := low[:e.Base]
		for _, in := range q.In {
			if filepath.IsAbs(in) {
				// a whole folder: "e:\work" matches e:\work\... but not e:\workshop
				if !hasPrefix(low, in) || len(low) > len(in) && low[len(in)] != '\\' && low[len(in)] != '/' && !strings.HasSuffix(in, `\`) && !strings.HasSuffix(in, "/") {
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
