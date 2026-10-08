package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestShorthands(t *testing.T) {
	now := time.Date(2026, 10, 8, 15, 30, 0, 0, time.Local)
	midnight := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local).Unix()
	cases := []struct {
		in    string
		check func(q *Query) bool
		desc  string
	}{
		{".pdf", func(q *Query) bool { return q.Exts["pdf"] && q.OnlyFile && !q.HasTerms() }, "PDF files"},
		{".jpg,.png", func(q *Query) bool { return q.Exts["jpg"] && q.Exts["png"] }, "JPG/PNG files"},
		{">10mb", func(q *Query) bool { return q.MinSize == 10<<20 && q.MaxSize == -1 }, "over 10 MB"},
		{"<1kb", func(q *Query) bool { return q.MaxSize == 1<<10 }, "under 1.0 KB"},
		{"1mb..1gb", func(q *Query) bool { return q.MinSize == 1<<20 && q.MaxSize == 1<<30 }, "1.0 MB to 1.0 GB"},
		{"<7d", func(q *Query) bool { return q.ModAfter == now.Unix()-7*86400 }, "changed in the last 7 days"},
		{">1y", func(q *Query) bool { return q.ModBefor == now.Unix()-365*86400 }, "not changed for 1 year"},
		{"today", func(q *Query) bool { return q.ModAfter == midnight && q.ModBefor == 0 }, "changed today"},
		{"yesterday", func(q *Query) bool { return q.ModAfter == midnight-86400 && q.ModBefor == midnight }, "changed yesterday"},
		{"week", func(q *Query) bool { return q.ModAfter == now.Unix()-7*86400 }, "changed this week"},
		{"photos/", func(q *Query) bool { return q.OnlyDirs && len(q.Terms) == 1 && string(q.Terms[0].text) == "photos" }, `folders like "photos"`},
		{"is:image", func(q *Query) bool { return q.Exts["heic"] && q.Exts["png"] && !q.Exts["mp4"] }, "images"},
		{"is:movies", func(q *Query) bool { return q.Exts["mkv"] }, "videos"},
		{"'today", func(q *Query) bool { return q.ModAfter == 0 && len(q.Terms) == 1 && q.Raw[0] == "today" }, `exactly "today"`},
		{"report .pdf >10mb week", func(q *Query) bool { return q.HasTerms() && q.Exts["pdf"] && q.MinSize > 0 && q.ModAfter > 0 },
			`"report" · PDF files · over 10 MB · changed this week`},
		// things that must stay plain search terms
		{"10mb", func(q *Query) bool { return len(q.Terms) == 1 && q.MinSize == 0 }, `"10mb"`},
		{"v1.2", func(q *Query) bool { return len(q.Terms) == 1 && len(q.Exts) == 0 }, `"v1.2"`},
		{"a..b", func(q *Query) bool { return len(q.Terms) == 1 }, `"a..b"`},
		{".5", func(q *Query) bool { return len(q.Terms) == 1 && len(q.Exts) == 0 }, `".5"`},
	}
	for _, c := range cases {
		q := ParseQuery(c.in, now)
		if q.Err != "" {
			t.Errorf("%q: unexpected error %s", c.in, q.Err)
			continue
		}
		if !c.check(q) {
			t.Errorf("%q: parsed wrong: %+v", c.in, q)
		}
		if got := q.Describe(); got != c.desc {
			t.Errorf("%q: described as %q, want %q", c.in, got, c.desc)
		}
	}
	if q := ParseQuery(">10zz", now); q.Err != "" || len(q.Terms) != 1 {
		t.Errorf(">10zz should be a plain term: %+v", q)
	}
	if q := ParseQuery("is:bogus", now); q.Err == "" {
		t.Error("is:bogus should report an error")
	}
}

func TestPathShorthand(t *testing.T) {
	root := t.TempDir()
	q := ParseQuery(root+" report", time.Now())
	if len(q.In) != 1 || len(q.Terms) != 1 || len(q.Exts) != 0 {
		t.Fatalf("absolute path should become an in: filter: %+v", q)
	}
	if runtime.GOOS == "windows" {
		// E:\work must not be read as the e: (ext) filter
		q = ParseQuery(`E:\work`, time.Now())
		if len(q.Exts) != 0 || len(q.In) != 1 || q.In[0] != `e:\work` {
			t.Fatalf("E:\\work parsed as %+v", q)
		}
	}
	home, _ := os.UserHomeDir()
	if q := ParseQuery("~/Documents", time.Now()); len(q.In) != 1 || q.In[0] != filepathLower(filepath.Join(home, "Documents")) {
		t.Errorf("~ not expanded: %+v", q.In)
	}
}

func filepathLower(p string) string { return string(asciiLower([]byte(p))) }

func TestShorthandSearch(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "work", "report.pdf"), "x")
	write(t, filepath.Join(dir, "workshop", "report.pdf"), "x")
	write(t, filepath.Join(dir, "photos", "beach.heic"), "x")
	write(t, filepath.Join(dir, "repo", ".gitignore"), "x")
	ix, err := BuildIndex([]string{dir}, defaultExcludes, nil)
	if err != nil {
		t.Fatal(err)
	}
	top := func(q string) []string {
		hits, _ := Search(ix, ParseQuery(q, time.Now()), SortRelevance, 10)
		return names(ix, hits)
	}
	if got := top(filepath.Join(dir, "work") + " .pdf"); len(got) != 1 {
		t.Errorf("in work only (not workshop): %v", got)
	}
	if got := top("is:image"); len(got) != 1 || got[0] != "beach.heic" {
		t.Errorf("is:image: %v", got)
	}
	if got := top("photos/"); len(got) != 1 || got[0] != "photos" {
		t.Errorf("photos/: %v", got)
	}
	if got := top(".gitignore"); len(got) != 1 || got[0] != ".gitignore" {
		t.Errorf(".gitignore (dotfile): %v", got)
	}
	if got := top("today .pdf"); len(got) != 2 {
		t.Errorf("today .pdf: %v", got)
	}
}
