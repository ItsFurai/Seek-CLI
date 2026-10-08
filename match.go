package main

import (
	"bytes"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"unsafe"
)

type SortMode int

const (
	SortRelevance SortMode = iota
	SortNewest
	SortLargest
	sortModes
)

func (s SortMode) String() string {
	return [...]string{"relevance", "newest", "largest"}[s]
}

type Hit struct {
	Idx uint32
	Key int64 // higher is better
	Len uint16
}

func better(a, b *Hit) bool {
	if a.Key != b.Key {
		return a.Key > b.Key
	}
	if a.Len != b.Len {
		return a.Len < b.Len
	}
	return a.Idx < b.Idx
}

// topK is a bounded min-heap: the root is the worst hit kept so far.
type topK struct {
	h []Hit
	k int
}

func (t *topK) push(x Hit) {
	if len(t.h) < t.k {
		t.h = append(t.h, x)
		i := len(t.h) - 1
		for i > 0 {
			p := (i - 1) / 2
			if !better(&t.h[p], &t.h[i]) {
				break
			}
			t.h[p], t.h[i] = t.h[i], t.h[p]
			i = p
		}
		return
	}
	if !better(&x, &t.h[0]) {
		return
	}
	t.h[0] = x
	i, n := 0, len(t.h)
	for {
		l, r, m := 2*i+1, 2*i+2, i
		if l < n && better(&t.h[m], &t.h[l]) {
			m = l
		}
		if r < n && better(&t.h[m], &t.h[r]) {
			m = r
		}
		if m == i {
			return
		}
		t.h[m], t.h[i] = t.h[i], t.h[m]
		i = m
	}
}

// Search scores every entry in parallel and returns the best `limit` hits plus the total match count.
func Search(ix *Index, q *Query, mode SortMode, limit int) ([]Hit, int) {
	if mode == SortRelevance && !q.HasTerms() {
		mode = SortNewest
	}
	n := len(ix.Entries)
	workers := runtime.NumCPU()
	const chunk = 16384
	var next atomic.Int64
	var total atomic.Int64
	heaps := make([]topK, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		heaps[w].k = limit
		wg.Add(1)
		go func(t *topK) {
			defer wg.Done()
			cnt := 0
			for {
				lo := int(next.Add(chunk)) - chunk
				if lo >= n {
					break
				}
				hi := min(lo+chunk, n)
				for i := lo; i < hi; i++ {
					e := &ix.Entries[i]
					if !q.passFilters(ix, e) {
						continue
					}
					score, ok := scoreEntry(ix, e, q)
					if !ok {
						continue
					}
					cnt++
					h := Hit{Idx: uint32(i), Len: e.Len}
					switch mode {
					case SortRelevance:
						h.Key = int64(score)
					case SortNewest:
						h.Key = e.Mod
					case SortLargest:
						h.Key = e.Size
					}
					t.push(h)
				}
			}
			total.Add(int64(cnt))
		}(&heaps[w])
	}
	wg.Wait()
	var all []Hit
	for _, t := range heaps {
		all = append(all, t.h...)
	}
	sort.Slice(all, func(i, j int) bool { return better(&all[i], &all[j]) })
	if len(all) > limit {
		all = all[:limit]
	}
	return all, int(total.Load())
}

func scoreEntry(ix *Index, e *Entry, q *Query) (int, bool) {
	low := ix.Lower(e)
	base := low[e.Base:]
	total, inBase := 0, true
	for i := range q.Terms {
		t := &q.Terms[i]
		switch t.kind {
		case termNot:
			if bytes.Contains(low, t.text) {
				return 0, false
			}
		case termExact:
			k := bytes.Index(low, t.text)
			if k < 0 {
				return 0, false
			}
			total += len(t.text) * 16
			if k >= int(e.Base) {
				total += 40
			} else {
				inBase = false
			}
		case termPrefix:
			if !bytes.HasPrefix(base, t.text) {
				return 0, false
			}
			total += len(t.text)*16 + 60
		case termSuffix:
			if !bytes.HasSuffix(low, t.text) {
				return 0, false
			}
			total += len(t.text) * 16
			inBase = inBase && len(t.text) <= len(base)
		default:
			orig := ix.arena[e.Off : e.Off+uint32(e.Len)]
			full, ok := fuzzy(low, orig, t.text, nil)
			if !ok {
				return 0, false
			}
			best := full
			if s, ok := fuzzy(base, orig[e.Base:], t.text, nil); ok && s+15 >= full {
				best = s + 15
				if len(base) == len(t.text) {
					best += 25 // exact basename hit
				}
			} else {
				inBase = false
			}
			total += best
		}
	}
	if inBase && len(q.Terms) > 1 {
		total += 50 // every term landed in the file name itself
	}
	// Prefer shallow/short paths.
	return total - int(e.Len)/12, true
}

const (
	scoreMatch       = 16
	bonusBoundary    = 12
	bonusCamel       = 8
	bonusConsecutive = 10
	penaltyGapStart  = 4
	penaltyGapExtend = 1
)

func isSep(c byte) bool {
	switch c {
	case '/', '\\', '_', '-', '.', ' ', '(', '[':
		return true
	}
	return false
}

// fuzzy finds pat as a subsequence of low (lowercased text), using the
// tightest window ending at the earliest possible end, and scores it.
// orig is the original-case text for camelCase boundaries. If pos is
// non-nil the matched offsets are appended to it.
func fuzzy(low, orig, pat []byte, pos *[]int) (int, bool) {
	if len(pat) == 0 {
		return 0, true
	}
	if len(pat) > len(low) {
		return 0, false
	}
	j, end := 0, -1
	for i := 0; i < len(low); i++ {
		if low[i] == pat[j] {
			j++
			if j == len(pat) {
				end = i + 1
				break
			}
		}
	}
	if end < 0 {
		return 0, false
	}
	j, start := len(pat)-1, 0
	for i := end - 1; i >= 0; i-- {
		if low[i] == pat[j] {
			j--
			if j < 0 {
				start = i
				break
			}
		}
	}
	score, prev := 0, -1
	j = 0
	for i := start; i < end && j < len(pat); i++ {
		if low[i] != pat[j] {
			continue
		}
		s := scoreMatch
		if i == 0 || isSep(low[i-1]) {
			s += bonusBoundary
		} else if orig[i] >= 'A' && orig[i] <= 'Z' && orig[i-1] >= 'a' && orig[i-1] <= 'z' {
			s += bonusCamel
		}
		if prev >= 0 {
			if gap := i - prev - 1; gap == 0 {
				s += bonusConsecutive
			} else {
				s -= penaltyGapStart + min(gap, 20)*penaltyGapExtend
			}
		}
		score += s
		prev = i
		if pos != nil {
			*pos = append(*pos, i)
		}
		j++
	}
	// Contiguous runs ("tui" in "tui.go") beat letters scattered across a long name.
	if span := end - start; span == len(pat) {
		score += 30
	} else if span <= 2*len(pat) {
		score += 10
	}
	return score, true
}

// MatchPositions returns byte offsets in the full path to highlight.
func MatchPositions(ix *Index, e *Entry, q *Query) map[int]bool {
	low := ix.Lower(e)
	orig := ix.arena[e.Off : e.Off+uint32(e.Len)]
	base := int(e.Base)
	set := map[int]bool{}
	for _, t := range q.Terms {
		var pos []int
		switch t.kind {
		case termExact, termSuffix:
			k := bytes.Index(low, t.text)
			if t.kind == termSuffix {
				k = len(low) - len(t.text)
			}
			for i := 0; k >= 0 && i < len(t.text); i++ {
				pos = append(pos, k+i)
			}
		case termPrefix:
			for i := range t.text {
				pos = append(pos, base+i)
			}
		case termFuzzy:
			// mirror scoreEntry's choice between basename and full path
			full, _ := fuzzy(low, orig, t.text, nil)
			var p []int
			if s, ok := fuzzy(low[base:], orig[base:], t.text, &p); ok && s+15 >= full {
				for _, x := range p {
					pos = append(pos, x+base)
				}
			} else {
				fuzzy(low, orig, t.text, &pos)
			}
		}
		for _, p := range pos {
			set[p] = true
		}
	}
	return set
}

func unsafeStr(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}
