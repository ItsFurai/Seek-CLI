package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

const indexMagic = "SEEKIDX3"

const flagDir = 1

// Entry is one indexed file or directory. Paths live in a shared byte arena
// so millions of entries cost a few allocations instead of millions.
type Entry struct {
	Off   uint32 // offset of path in arena
	Len   uint16 // path length in bytes
	Base  uint16 // offset of basename within the path
	Flags uint8
	Size  int64
	Mod   int64 // unix seconds
}

type Index struct {
	Created  time.Time
	Roots    []string
	Excludes []string
	Entries  []Entry
	arena    []byte // original-case paths
	lower    []byte // ASCII-lowercased copy, same offsets
	unsorted bool   // entries added by live updates are appended, not sorted
}

func (ix *Index) Path(e *Entry) string {
	if e.Len == 0 {
		return ""
	}
	return unsafe.String(&ix.arena[e.Off], int(e.Len))
}

func (ix *Index) Lower(e *Entry) []byte { return ix.lower[e.Off : e.Off+uint32(e.Len)] }

func (ix *Index) Name(e *Entry) string { return ix.Path(e)[e.Base:] }

func (e *Entry) IsDir() bool { return e.Flags&flagDir != 0 }

func (ix *Index) Files() (files, dirs int) {
	for i := range ix.Entries {
		if ix.Entries[i].IsDir() {
			dirs++
		} else {
			files++
		}
	}
	return
}

func indexPath() string {
	if p := os.Getenv("SEEK_INDEX"); p != "" {
		return p
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "seek", "index.bin")
}

var defaultExcludes = []string{
	"$Recycle.Bin", "System Volume Information", "$WinREAgent", "Config.Msi",
	".git", "node_modules", "__pycache__", ".cache", "proc", "sys",
}

// ---------- building ----------

type rawEntry struct {
	path  string
	size  int64
	mod   int64
	isDir bool
}

type dirQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []string
	pending int
}

func (q *dirQueue) push(d string) {
	q.mu.Lock()
	q.items = append(q.items, d)
	q.pending++
	q.mu.Unlock()
	q.cond.Signal()
}

func (q *dirQueue) pop() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && q.pending > 0 {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		return "", false
	}
	d := q.items[len(q.items)-1]
	q.items = q.items[:len(q.items)-1]
	return d, true
}

func (q *dirQueue) done() {
	q.mu.Lock()
	q.pending--
	if q.pending == 0 {
		q.cond.Broadcast()
	}
	q.mu.Unlock()
}

func excludeSet(excludes []string) map[string]bool {
	ex := make(map[string]bool, len(excludes))
	for _, e := range excludes {
		ex[strings.ToLower(e)] = true
	}
	return ex
}

// BuildIndex walks roots in parallel. progress (may be nil) is incremented per entry.
func BuildIndex(roots, excludes []string, progress *atomic.Int64) (*Index, error) {
	var all []rawEntry
	var starts []string
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		st, err := os.Stat(abs)
		if err != nil || !st.IsDir() {
			continue
		}
		all = append(all, rawEntry{path: abs, mod: st.ModTime().Unix(), isDir: true})
		starts = append(starts, abs)
	}
	if len(all) == 0 {
		return nil, errors.New("no valid root directories to index")
	}
	all = append(all, walkDirs(starts, excludeSet(excludes), progress)...)
	return fromRaw(all, roots, excludes), nil
}

// walkDirs returns everything below the given directories (not the directories themselves).
func walkDirs(starts []string, ex map[string]bool, progress *atomic.Int64) []rawEntry {
	q := &dirQueue{}
	q.cond = sync.NewCond(&q.mu)
	for _, s := range starts {
		q.push(s)
	}
	workers := runtime.NumCPU() * 2
	if workers < 4 {
		workers = 4
	}
	results := make([][]rawEntry, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			local := make([]rawEntry, 0, 256)
			for {
				dir, ok := q.pop()
				if !ok {
					break
				}
				ents, _ := os.ReadDir(dir) // partial results on permission errors are fine
				for _, d := range ents {
					name := d.Name()
					if ex[strings.ToLower(name)] {
						continue
					}
					full := filepath.Join(dir, name)
					if len(full) > 65535 {
						continue
					}
					re := rawEntry{path: full, isDir: d.IsDir()}
					if info, err := d.Info(); err == nil {
						re.mod = info.ModTime().Unix()
						if !re.isDir {
							re.size = info.Size()
						}
					}
					local = append(local, re)
					if progress != nil {
						progress.Add(1)
					}
					// d.IsDir() is false for symlinks/junctions, so no loops.
					if re.isDir {
						q.push(full)
					}
				}
				q.done()
			}
			results[w] = local
		}(w)
	}
	wg.Wait()
	var all []rawEntry
	for _, r := range results {
		all = append(all, r...)
	}
	return all
}
func fromRaw(raw []rawEntry, roots, excludes []string) *Index {
	// Sorted paths give a stable tie-break order and let Save prefix-compress.
	slices.SortFunc(raw, func(a, b rawEntry) int { return strings.Compare(a.path, b.path) })
	total := 0
	for i := range raw {
		total += len(raw[i].path)
	}
	ix := &Index{
		Created:  time.Now(),
		Roots:    roots,
		Excludes: excludes,
		Entries:  make([]Entry, 0, len(raw)),
		arena:    make([]byte, 0, total),
	}
	for i := range raw {
		r := &raw[i]
		e := Entry{Off: uint32(len(ix.arena)), Len: uint16(len(r.path)), Size: r.size, Mod: r.mod}
		e.Base = uint16(baseOffset(r.path))
		if r.isDir {
			e.Flags |= flagDir
		}
		ix.arena = append(ix.arena, r.path...)
		ix.Entries = append(ix.Entries, e)
	}
	ix.lower = asciiLower(ix.arena)
	return ix
}

func baseOffset(p string) int {
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 || i == len(p)-1 {
		return 0
	}
	return i + 1
}

func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	const chunk = 4 << 20
	if len(b) <= chunk {
		lowerInto(out, b)
		return out
	}
	var wg sync.WaitGroup
	for lo := 0; lo < len(b); lo += chunk {
		hi := min(lo+chunk, len(b))
		wg.Add(1)
		go func() { defer wg.Done(); lowerInto(out[lo:hi], b[lo:hi]) }()
	}
	wg.Wait()
	return out
}

func lowerInto(dst, src []byte) {
	for i, c := range src {
		if c-'A' < 26 {
			c += 'a' - 'A'
		}
		dst[i] = c
	}
}

// ---------- persistence ----------

func (ix *Index) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	var buf [binary.MaxVarintLen64]byte
	uv := func(v uint64) { n := binary.PutUvarint(buf[:], v); w.Write(buf[:n]) }
	sv := func(v int64) { n := binary.PutVarint(buf[:], v); w.Write(buf[:n]) }
	str := func(s string) { uv(uint64(len(s))); w.WriteString(s) }

	w.WriteString(indexMagic)
	sv(ix.Created.Unix())
	uv(uint64(len(ix.Roots)))
	for _, r := range ix.Roots {
		str(r)
	}
	uv(uint64(len(ix.Excludes)))
	for _, e := range ix.Excludes {
		str(e)
	}
	// Live updates append entries out of order and leave dead bytes in the
	// arena, so write in sorted order and count only live path bytes.
	order := make([]uint32, len(ix.Entries))
	live := 0
	for i := range order {
		order[i] = uint32(i)
		live += int(ix.Entries[i].Len)
	}
	if ix.unsorted {
		slices.SortFunc(order, func(a, b uint32) int {
			return strings.Compare(ix.Path(&ix.Entries[a]), ix.Path(&ix.Entries[b]))
		})
	}
	uv(uint64(len(ix.Entries)))
	uv(uint64(live))
	prev := ""
	for _, i := range order {
		e := &ix.Entries[i]
		p := ix.Path(e)
		k := 0
		for k < len(p) && k < len(prev) && k < 65535 && p[k] == prev[k] {
			k++
		}
		uv(uint64(k))
		str(p[k:])
		prev = p
		w.WriteByte(e.Flags)
		uv(uint64(e.Size))
		sv(e.Mod)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type reader struct {
	b   []byte
	pos int
	err error
}

func (r *reader) uv() uint64 {
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 {
		r.err = io.ErrUnexpectedEOF
		r.pos = len(r.b)
		return 0
	}
	r.pos += n
	return v
}

func (r *reader) sv() int64 {
	v, n := binary.Varint(r.b[r.pos:])
	if n <= 0 {
		r.err = io.ErrUnexpectedEOF
		r.pos = len(r.b)
		return 0
	}
	r.pos += n
	return v
}

func (r *reader) bytes() (int, int) {
	n := int(r.uv())
	if r.err != nil || r.pos+n > len(r.b) {
		r.err = io.ErrUnexpectedEOF
		return 0, 0
	}
	start := r.pos
	r.pos += n
	return start, n
}

func (r *reader) str() string {
	s, n := r.bytes()
	return string(r.b[s : s+n])
}

// LoadIndex reads the file in one shot and points entries straight into it.
func LoadIndex(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < len(indexMagic) || string(data[:len(indexMagic)]) != indexMagic {
		return nil, fmt.Errorf("%s is not a seek index (or is from an older version); run `seek index`", path)
	}
	r := &reader{b: data, pos: len(indexMagic)}
	ix := &Index{}
	ix.Created = time.Unix(r.sv(), 0)
	for n := r.uv(); n > 0 && r.err == nil; n-- {
		ix.Roots = append(ix.Roots, r.str())
	}
	for n := r.uv(); n > 0 && r.err == nil; n-- {
		ix.Excludes = append(ix.Excludes, r.str())
	}
	count := r.uv()
	if count > uint64(len(data)) {
		return nil, errors.New("corrupt index")
	}
	arenaLen := r.uv()
	if count > uint64(len(data)) || arenaLen > 1<<32-1 {
		return nil, errors.New("corrupt index")
	}
	ix.Entries = make([]Entry, 0, count)
	arena := make([]byte, 0, arenaLen)
	prevOff, prevLen := 0, 0
	for i := uint64(0); i < count && r.err == nil; i++ {
		shared := int(r.uv())
		off, n := r.bytes()
		if r.err != nil || r.pos >= len(data) || shared > prevLen {
			r.err = io.ErrUnexpectedEOF
			break
		}
		start := len(arena)
		arena = append(arena, arena[prevOff:prevOff+shared]...)
		arena = append(arena, data[off:off+n]...)
		flags := data[r.pos]
		r.pos++
		size := int64(r.uv())
		mod := r.sv()
		plen := shared + n
		p := unsafe.String(&arena[start], plen)
		ix.Entries = append(ix.Entries, Entry{
			Off: uint32(start), Len: uint16(plen), Base: uint16(baseOffset(p)),
			Flags: flags, Size: size, Mod: mod,
		})
		prevOff, prevLen = start, plen
	}
	if r.err != nil || len(arena) != int(arenaLen) {
		return nil, fmt.Errorf("corrupt index (%v); run `seek index`", r.err)
	}
	ix.arena = arena
	ix.lower = asciiLower(arena)
	return ix, nil
}
