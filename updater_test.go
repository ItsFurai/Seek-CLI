package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

func paths(ix *Index) []string {
	var out []string
	for i := range ix.Entries {
		out = append(out, ix.Path(&ix.Entries[i]))
	}
	sort.Strings(out)
	return out
}

func has(ix *Index, p string) bool {
	for i := range ix.Entries {
		if ix.Path(&ix.Entries[i]) == p {
			return true
		}
	}
	return false
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestApplyChanges(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "a")
	write(t, filepath.Join(dir, "old", "inner.txt"), "x")
	write(t, filepath.Join(dir, "keep", "k.txt"), "k")
	ix, err := BuildIndex([]string{dir}, defaultExcludes, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := len(ix.Entries)
	ex := excludeSet(defaultExcludes)

	// create a file, delete a file, add a folder tree, rename a folder,
	// add to an excluded folder, and touch an existing folder
	write(t, filepath.Join(dir, "new.go"), "package x")
	os.Remove(filepath.Join(dir, "a.txt"))
	write(t, filepath.Join(dir, "tree", "sub", "deep.md"), "# hi")
	os.Rename(filepath.Join(dir, "old"), filepath.Join(dir, "renamed"))
	write(t, filepath.Join(dir, "node_modules", "pkg.js"), "x")
	write(t, filepath.Join(dir, "keep", "k2.txt"), "k2")

	changed := map[string]bool{
		filepath.Join(dir, "new.go"):                 true,
		filepath.Join(dir, "a.txt"):                  true,
		filepath.Join(dir, "tree"):                   true, // only the top folder is reported
		filepath.Join(dir, "tree", "sub", "deep.md"): true, // ...and sometimes a child too
		filepath.Join(dir, "old"):                    true,
		filepath.Join(dir, "renamed"):                true,
		filepath.Join(dir, "node_modules"):           true,
		filepath.Join(dir, "keep"):                   true, // folder mtime changed
		filepath.Join(dir, "keep", "k2.txt"):         true,
	}
	nx, _ := ix.applyChanges(changed, ex)

	mustHave := []string{"new.go", "tree", filepath.Join("tree", "sub"), filepath.Join("tree", "sub", "deep.md"),
		"renamed", filepath.Join("renamed", "inner.txt"), "keep", filepath.Join("keep", "k.txt"), filepath.Join("keep", "k2.txt")}
	for _, p := range mustHave {
		if !has(nx, filepath.Join(dir, p)) {
			t.Errorf("missing %s; have %v", p, paths(nx))
		}
	}
	for _, p := range []string{"a.txt", "old", filepath.Join("old", "inner.txt"), "node_modules", filepath.Join("node_modules", "pkg.js")} {
		if has(nx, filepath.Join(dir, p)) {
			t.Errorf("should be gone: %s", p)
		}
	}
	// no duplicates
	ps := paths(nx)
	for i := 1; i < len(ps); i++ {
		if ps[i] == ps[i-1] {
			t.Errorf("duplicate entry %s", ps[i])
		}
	}
	// the original version must be untouched (searches may still be using it)
	if len(ix.Entries) != before || !has(ix, filepath.Join(dir, "a.txt")) {
		t.Error("applyChanges modified the old index")
	}

	// saving an updated index must round-trip
	p := filepath.Join(t.TempDir(), "idx.bin")
	if err := nx.Save(p); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIndex(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := paths(loaded), paths(nx); len(got) != len(want) {
		t.Fatalf("round trip: %d vs %d entries", len(got), len(want))
	}
	hits, _ := Search(loaded, ParseQuery("deep", time.Now()), SortRelevance, 5)
	if len(hits) == 0 || loaded.Name(&loaded.Entries[hits[0].Idx]) != "deep.md" {
		t.Error("search after reload failed")
	}
}

func TestLiveWatcher(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("live watching is Windows-only")
	}
	t.Setenv("SEEK_INDEX", filepath.Join(t.TempDir(), "idx.bin"))
	dir := t.TempDir()
	write(t, filepath.Join(dir, "start.txt"), "s")
	ix, err := BuildIndex([]string{dir}, defaultExcludes, nil)
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan *Index, 16)
	u := NewUpdater(ix, []string{dir}, defaultExcludes)
	u.OnUpdate = func(ix *Index, n int, full bool) { updates <- ix }
	u.Start()
	if !u.Live {
		t.Fatal("watcher did not start")
	}

	write(t, filepath.Join(dir, "created.txt"), "c")
	write(t, filepath.Join(dir, "folder", "nested.txt"), "n")
	os.Remove(filepath.Join(dir, "start.txt"))

	want := func(ix *Index) bool {
		return has(ix, filepath.Join(dir, "created.txt")) &&
			has(ix, filepath.Join(dir, "folder", "nested.txt")) &&
			!has(ix, filepath.Join(dir, "start.txt"))
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ix := <-updates:
			if want(ix) {
				u.Close()
				if _, err := os.Stat(os.Getenv("SEEK_INDEX")); err != nil {
					t.Errorf("index was not saved on close: %v", err)
				}
				return
			}
		case <-deadline:
			u.Close()
			t.Fatalf("live index never caught up; have %v", paths(u.Index()))
		}
	}
}
