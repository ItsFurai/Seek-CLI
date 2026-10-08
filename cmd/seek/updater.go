package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// applyChanges returns a new Index reflecting the current on-disk state of the
// changed paths. ix is never modified: the new version shares ix's path arena
// and only appends past its end, so searches still running on ix are safe.
func (ix *Index) applyChanges(changed map[string]bool, ex map[string]bool) (*Index, int) {
	// Pass 1: what does the index currently know about each changed path?
	wasDir := map[string]bool{}
	for i := range ix.Entries {
		e := &ix.Entries[i]
		if p := ix.Path(e); changed[p] {
			wasDir[p] = e.IsDir()
		}
	}

	// Classify. A folder that still exists only gets its own entry refreshed;
	// its children report their own events. A folder that is gone loses its
	// whole subtree, and a folder that is new (created or moved in) is walked.
	tree := map[string]bool{} // remove p and everything below it
	self := map[string]bool{} // remove just p
	var add []rawEntry
	var walk []string
	for p := range changed {
		st, err := os.Lstat(p)
		known, inIndex := wasDir[p]
		switch {
		case err != nil:
			tree[p] = true
		case st.IsDir() && !(inIndex && known):
			tree[p] = true
			if !excludedPath(p, ex) {
				add = append(add, rawEntry{path: p, mod: st.ModTime().Unix(), isDir: true})
				walk = append(walk, p)
			}
		default:
			if inIndex && known && !st.IsDir() {
				tree[p] = true // folder replaced by a file
			} else {
				self[p] = true
			}
			if !excludedPath(p, ex) {
				re := rawEntry{path: p, mod: st.ModTime().Unix(), isDir: st.IsDir()}
				if !re.isDir {
					re.size = st.Size()
				}
				add = append(add, re)
			}
		}
	}
	// Paths inside a removed/rewalked tree are already covered by it.
	minLen := 1 << 30
	for p := range tree {
		minLen = min(minLen, len(p))
	}
	filtered := add[:0]
	for _, r := range add {
		if !tree[r.path] && underTree(r.path, tree, minLen) {
			continue
		}
		filtered = append(filtered, r)
	}
	walkTop := walk[:0]
	for _, w := range walk {
		if !underTree(w, tree, minLen) {
			walkTop = append(walkTop, w)
		}
	}
	add = append(filtered, walkDirs(walkTop, ex, nil)...)

	// Pass 2: copy surviving entries.
	kept := make([]Entry, 0, len(ix.Entries)+len(add))
	for i := range ix.Entries {
		e := &ix.Entries[i]
		p := ix.Path(e)
		if self[p] || len(p) >= minLen && (tree[p] || underTree(p, tree, minLen)) {
			continue
		}
		kept = append(kept, *e)
	}

	nx := &Index{Created: ix.Created, Roots: ix.Roots, Excludes: ix.Excludes, unsorted: true}
	arena, lower := ix.arena, ix.lower
	live := 0
	for i := range kept {
		live += int(kept[i].Len)
	}
	for _, r := range add {
		if len(r.path) > 65535 {
			continue
		}
		e := Entry{Off: uint32(len(arena)), Len: uint16(len(r.path)), Base: uint16(baseOffset(r.path)), Size: r.size, Mod: r.mod}
		if r.isDir {
			e.Flags |= flagDir
		}
		arena = append(arena, r.path...)
		lower = append(lower, asciiLower([]byte(r.path))...)
		kept = append(kept, e)
		live += len(r.path)
	}
	nx.Entries, nx.arena, nx.lower = kept, arena, lower
	if len(arena) > 2*live+(32<<20) || len(arena) > 3<<30 {
		nx = nx.compact()
	}
	return nx, len(changed)
}

// underTree reports whether p is strictly below any path in tree.
func underTree(p string, tree map[string]bool, minLen int) bool {
	for {
		i := strings.LastIndexAny(p, `\/`)
		if i <= 0 || i < minLen {
			return false
		}
		p = p[:i]
		if tree[p] {
			return true
		}
	}
}

func excludedPath(p string, ex map[string]bool) bool {
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '\\' || r == '/' }) {
		if ex[strings.ToLower(part)] {
			return true
		}
	}
	return false
}

// compact copies live paths into a fresh arena, dropping bytes left behind by deletions.
func (ix *Index) compact() *Index {
	raw := make([]rawEntry, len(ix.Entries))
	for i := range ix.Entries {
		e := &ix.Entries[i]
		raw[i] = rawEntry{path: ix.Path(e), size: e.Size, mod: e.Mod, isDir: e.IsDir()}
	}
	nx := fromRaw(raw, ix.Roots, ix.Excludes)
	nx.Created = ix.Created
	return nx
}

// ---------- background updater ----------

const (
	staleAfter      = 30 * time.Minute // rebuild on start if the saved index is older
	pollRebuild     = 10 * time.Minute // rescan interval where live watching isn't available
	saveEvery       = 30 * time.Second
	debounceQuiet   = 300 * time.Millisecond
	debounceMax     = 2 * time.Second
	maxPendingPaths = 200_000 // beyond this a full rebuild is cheaper
)

type rebuildResult struct {
	ix  *Index
	err error
}

// Updater keeps an index current: it applies file-system change events in
// small batches, rebuilds when events are lost, and saves periodically.
type Updater struct {
	Live bool // true when change events are being received

	path     string
	roots    []string
	excludes []string
	ex       map[string]bool
	ignore   string // lowercased folder holding the index file itself

	events   chan string
	overflow chan struct{}
	rebuild  chan struct{}
	stop     chan struct{}
	done     chan struct{}

	OnUpdate       func(ix *Index, changes int, full bool)
	OnRebuildStart func(progress *atomic.Int64)
	OnError        func(error)

	mu    sync.Mutex
	ix    *Index
	dirty bool
}

func NewUpdater(ix *Index, roots, excludes []string) *Updater {
	return &Updater{
		path: indexPath(), roots: roots, excludes: excludes, ex: excludeSet(excludes),
		ignore:   strings.ToLower(filepath.Dir(indexPath())),
		events:   make(chan string, 1<<16),
		overflow: make(chan struct{}, 1),
		rebuild:  make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		ix:       ix,
	}
}

// Start begins watching. A rebuild runs first if there is no index or it is stale.
func (u *Updater) Start() {
	u.Live = watchRoots(u.roots, u.events, u.overflow)
	needRebuild := u.ix == nil || time.Since(u.ix.Created) > staleAfter
	go u.run(needRebuild)
}

// Rebuild requests a full rescan.
func (u *Updater) Rebuild() {
	select {
	case u.rebuild <- struct{}{}:
	default:
	}
}

// Close stops the updater and saves unsaved changes.
func (u *Updater) Close() {
	close(u.stop)
	<-u.done
}

func (u *Updater) Index() *Index {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.ix
}

func (u *Updater) run(needRebuild bool) {
	defer close(u.done)
	pending := map[string]bool{}
	var firstPending, lastEvent time.Time
	var rebuilding chan rebuildResult
	lastSave := time.Now()

	startRebuild := func() {
		if rebuilding != nil {
			return
		}
		prog := &atomic.Int64{}
		if u.OnRebuildStart != nil {
			u.OnRebuildStart(prog)
		}
		ch := make(chan rebuildResult, 1)
		go func() {
			ix, err := BuildIndex(u.roots, u.excludes, prog)
			ch <- rebuildResult{ix, err}
		}()
		rebuilding = ch
	}
	save := func() {
		u.mu.Lock()
		ix, dirty := u.ix, u.dirty
		u.dirty = false
		u.mu.Unlock()
		if ix == nil || !dirty {
			return
		}
		cp := *ix
		if u.Live {
			cp.Created = time.Now() // with live updates, the index is accurate as of now
		}
		if err := cp.Save(u.path); err != nil && u.OnError != nil {
			u.OnError(err)
		}
		lastSave = time.Now()
	}
	publish := func(ix *Index, n int, full bool) {
		u.mu.Lock()
		u.ix, u.dirty = ix, true
		u.mu.Unlock()
		if u.OnUpdate != nil {
			u.OnUpdate(ix, n, full)
		}
	}

	if needRebuild {
		startRebuild()
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var poll <-chan time.Time
	if !u.Live {
		t := time.NewTicker(pollRebuild)
		defer t.Stop()
		poll = t.C
	}

	for {
		select {
		case p := <-u.events:
			if strings.HasPrefix(strings.ToLower(p), u.ignore) || excludedPath(p, u.ex) {
				continue
			}
			if len(pending) == 0 {
				firstPending = time.Now()
			}
			pending[p] = true
			lastEvent = time.Now()
			if len(pending) > maxPendingPaths {
				pending = map[string]bool{}
				startRebuild()
			}

		case <-u.overflow:
			startRebuild() // events were dropped; only a rescan is trustworthy

		case <-u.rebuild:
			startRebuild()

		case <-poll:
			startRebuild()

		case r := <-rebuilding:
			rebuilding = nil
			if r.err != nil {
				if u.OnError != nil {
					u.OnError(r.err)
				}
				continue
			}
			ix := r.ix
			if len(pending) > 0 { // replay what changed while the rescan ran
				ix, _ = ix.applyChanges(pending, u.ex)
				pending = map[string]bool{}
			}
			publish(ix, 0, true)
			save()

		case <-tick.C:
			if rebuilding == nil && len(pending) > 0 && u.Index() != nil &&
				(time.Since(lastEvent) > debounceQuiet || time.Since(firstPending) > debounceMax) {
				nx, n := u.Index().applyChanges(pending, u.ex)
				pending = map[string]bool{}
				publish(nx, n, false)
			}
			if time.Since(lastSave) > saveEvery {
				save()
			}

		case <-u.stop:
			save()
			return
		}
	}
}
