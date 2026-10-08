package main

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

type ContentHit struct {
	Idx  uint32
	Line int // 1-based
	Col  int // byte offset of match within Text
	MLen int
	Text string
}

const maxGrepFileSize = 16 << 20

var binaryExts = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`exe dll sys bin obj o a lib so dylib pdb iso img vhd vhdx dmg msi cab
		zip 7z rar gz tgz bz2 xz zst tar jar war apk ipa nupkg whl
		png jpg jpeg gif bmp ico tif tiff webp heic psd raw cr2 nef avif
		mp3 wav flac ogg m4a aac wma mp4 mkv avi mov wmv webm flv m4v
		pdf doc docx xls xlsx ppt pptx odt ods odp epub mobi
		ttf otf woff woff2 eot class pyc pyd db sqlite mdb accdb dat pak blob
		lnk node wasm`) {
		binaryExts[e] = true
	}
}

type grepMatcher struct {
	lower bool   // match against an ASCII-lowercased copy of the file
	lit   []byte // literal pattern (nil when using regex)
	re    *regexp.Regexp
}

func (m *grepMatcher) find(line []byte) (int, int, bool) {
	if m.re != nil {
		loc := m.re.FindIndex(line)
		if loc == nil || loc[1] == loc[0] {
			return 0, 0, false
		}
		return loc[0], loc[1] - loc[0], true
	}
	i := bytes.Index(line, m.lit)
	return i, len(m.lit), i >= 0
}

// newMatcher uses smart case: case-insensitive unless the pattern has an uppercase letter.
func newMatcher(pattern string, useRegex bool) (*grepMatcher, error) {
	caseSensitive := strings.IndexFunc(pattern, unicode.IsUpper) >= 0
	if useRegex {
		if !caseSensitive {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		return &grepMatcher{re: re}, nil
	}
	if caseSensitive {
		return &grepMatcher{lit: []byte(pattern)}, nil
	}
	return &grepMatcher{lower: true, lit: asciiLower([]byte(pattern))}, nil
}

// Grep searches file contents of every entry that passes q's filters.
// emit is called with batches of hits (from a single goroutine).
func Grep(ctx context.Context, ix *Index, q *Query, pattern string, useRegex bool, maxHits int,
	emit func([]ContentHit), scanned *atomic.Int64) error {

	match, err := newMatcher(pattern, useRegex)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan uint32, 1024)
	out := make(chan ContentHit, 1024)
	var found atomic.Int64

	go func() {
		defer close(jobs)
		for i := range ix.Entries {
			e := &ix.Entries[i]
			if e.IsDir() || e.Size == 0 || e.Size > maxGrepFileSize || binaryExts[extOf(ix.Name(e))] || !q.passFilters(ix, e) {
				continue
			}
			select {
			case jobs <- uint32(i):
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if ctx.Err() != nil {
					continue
				}
				grepFile(ctx, ix.Path(&ix.Entries[idx]), idx, match, out, &found, maxHits, cancel)
				if scanned != nil {
					scanned.Add(1)
				}
			}
		}()
	}
	go func() { wg.Wait(); close(out) }()

	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	var batch []ContentHit
	for {
		select {
		case h, ok := <-out:
			if !ok {
				if len(batch) > 0 {
					emit(batch)
				}
				return nil
			}
			batch = append(batch, h)
		case <-tick.C:
			if len(batch) > 0 {
				emit(batch)
				batch = nil
			}
		}
	}
}

func grepFile(ctx context.Context, path string, idx uint32, match *grepMatcher, out chan<- ContentHit,
	found *atomic.Int64, maxHits int, stop func()) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return // binary
	}
	hay := data
	if match.lower {
		hay = asciiLower(data)
	}
	if match.lit != nil && !bytes.Contains(hay, match.lit) {
		return // fast reject: one SIMD scan over the whole file
	}
	const perFile = 100
	n, line, pos := 0, 0, 0
	for pos < len(data) && n < perFile {
		line++
		nl := bytes.IndexByte(data[pos:], '\n')
		end := len(data)
		if nl >= 0 {
			end = pos + nl
		}
		l, hl := data[pos:end], hay[pos:end]
		pos = end + 1
		col, mlen, ok := match.find(hl)
		if !ok {
			continue
		}
		text, col := snippet(l, col)
		select {
		case out <- ContentHit{Idx: idx, Line: line, Col: col, MLen: mlen, Text: text}:
		case <-ctx.Done():
			return
		}
		n++
		if found.Add(1) >= int64(maxHits) {
			stop()
			return
		}
	}
}

// snippet trims and clips a long line around the match, returning the new match column.
func snippet(l []byte, col int) (string, int) {
	l = bytes.TrimRight(l, "\r")
	start := 0
	for start < col && (l[start] == ' ' || l[start] == '\t') {
		start++
	}
	if col-start > 80 {
		start = col - 40
	}
	end := min(len(l), start+400)
	s := strings.ReplaceAll(string(l[start:end]), "\t", "  ")
	// tab expansion shifts the column
	shift := strings.Count(string(l[start:col]), "\t")
	return strings.ToValidUTF8(s, "?"), col - start + shift
}
