package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testIndex(t *testing.T) *Index {
	dir := t.TempDir()
	files := map[string]string{
		"src/main.go":           "package main\nfunc main() {}\n",
		"src/ui/tui.go":         "package ui\n// TODO render\n",
		"docs/README.md":        "# Hello\nsee TODO list\n",
		"docs/big.bin":          "\x00\x01binary TODO",
		"node_modules/x/tui.js": "skipped",
	}
	for p, c := range files {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	ix, err := BuildIndex([]string{dir}, defaultExcludes, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func names(ix *Index, hits []Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, ix.Name(&ix.Entries[h.Idx]))
	}
	return out
}

func TestSearchAndRoundTrip(t *testing.T) {
	ix := testIndex(t)
	p := filepath.Join(t.TempDir(), "idx.bin")
	if err := ix.Save(p); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIndex(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Entries) != len(ix.Entries) {
		t.Fatalf("round trip: %d vs %d entries", len(loaded.Entries), len(ix.Entries))
	}
	for i := range ix.Entries {
		if ix.Path(&ix.Entries[i]) != loaded.Path(&loaded.Entries[i]) {
			t.Fatalf("path %d differs", i)
		}
	}

	now := time.Now()
	cases := []struct {
		q    string
		want string // expected top hit ("" = no hits)
	}{
		{"tui", "tui.go"},
		{"main go", "main.go"},
		{"readme ext:md", "README.md"},
		{"is:dir ui", "ui"},
		{"'tui !ui", ""},
		{"^read", "README.md"},
		{"size:>1kb", ""},
	}
	for _, c := range cases {
		hits, _ := Search(loaded, ParseQuery(c.q, now), SortRelevance, 10)
		got := ""
		if len(hits) > 0 {
			got = names(loaded, hits)[0]
		}
		if got != c.want {
			t.Errorf("%q: top hit %q, want %q (all: %v)", c.q, got, c.want, names(loaded, hits))
		}
	}
	for i := range loaded.Entries {
		if filepath.Base(loaded.Path(&loaded.Entries[i])) == "node_modules" {
			t.Error("node_modules should be excluded")
		}
	}
}

func TestParseQuery(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	q := ParseQuery("foo ext:.GO,rs size:1mb..2gb mod:<7d", now)
	if len(q.Terms) != 1 || !q.Exts["go"] || !q.Exts["rs"] {
		t.Fatalf("terms/exts: %+v", q)
	}
	if q.MinSize != 1<<20 || q.MaxSize != 2<<30 {
		t.Errorf("size: %d..%d", q.MinSize, q.MaxSize)
	}
	if q.ModAfter != now.Unix()-7*86400 {
		t.Errorf("mod: %d", q.ModAfter)
	}
	if ParseQuery("size:>abc", now).Err == "" {
		t.Error("expected error for bad size")
	}
}

func TestGrep(t *testing.T) {
	ix := testIndex(t)
	for _, pat := range []string{"todo", "TODO"} { // smart case: both hit the same two files
		var hits []ContentHit
		err := Grep(t.Context(), ix, ParseQuery("", time.Now()), pat, false, 100,
			func(b []ContentHit) { hits = append(hits, b...) }, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 2 { // tui.go + README.md; the binary file is skipped
			t.Fatalf("%q: got %d hits: %+v", pat, len(hits), hits)
		}
	}
}
