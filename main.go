package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
)

const usage = `seek — fast file search & indexing with a terminal UI

USAGE
  seek [query]              open the interactive search UI (optionally prefilled)
  seek -c [text]            open the UI in content (grep) mode
  seek pick [query]         open the UI; enter prints the chosen path instead of opening it
                              PowerShell:  cd (seek pick)
  seek index [dirs...]      build the index (default: all fixed drives)
      --exclude a,b           extra folder names to skip
      --all                   don't skip the default folders (.git, node_modules, …)
  seek find <query>         print matching paths    -n N  limit (default 50)
                                                     --sort relevance|newest|largest
  seek grep <text> [filters] search inside files    -r  regex   -n N  limit (default 500)
  seek stats                show index information
  seek watch [-v]           keep the index up to date in the background (Ctrl+C to stop)
  seek --version

QUERY SYNTAX
  report budget    fuzzy words (all must match; file names rank higher)
  .pdf .jpg,.png   file type          is:image  (video audio doc code archive app dir file)
  >10mb <1kb       size               1mb..1gb
  today yesterday  changed on day     week month year  (last 7/30/365 days)
  <7d >1y          changed within / not changed for   (min h d w mo y)
  photos/          folders named like photos
  E:\work ~\Docs   only inside that folder
  'foo ^foo foo$   exact / name starts with / ends with      !foo  exclude
  The older key:value forms still work: ext:pdf size:>10mb mod:<7d in:E:\work
  In a shell, quote > and < so they aren't redirects:  seek find report "'>10mb'"
Index file: %s  (override with SEEK_INDEX)
`

// version is set at build time: -ldflags "-X main.version=v1.2.3"
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "seek:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Printf(usage, indexPath())
			return nil
		case "-v", "--version", "version":
			fmt.Println("seek", version)
			return nil
		case "index":
			return cmdIndex(args[1:])
		case "find":
			return cmdFind(args[1:])
		case "grep":
			return cmdGrep(args[1:])
		case "stats":
			return cmdStats()
		case "watch":
			return cmdWatch(args[1:])
		case "pick":
			return runTUI(loadOrNil(), strings.Join(args[1:], " "), modeName, true)
		case "-c", "--content":
			return runTUI(loadOrNil(), strings.Join(args[1:], " "), modeContent, false)
		}
	}
	return runTUI(loadOrNil(), strings.Join(args, " "), modeName, false)
}

// loadOrNil returns nil when no index exists yet; the UI then builds one.
func loadOrNil() *Index {
	ix, err := LoadIndex(indexPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "seek:", err, "— rebuilding")
		}
		return nil
	}
	return ix
}

func mustLoad() (*Index, error) {
	ix, err := LoadIndex(indexPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("no index yet — run `seek index` (or just `seek`) first")
	}
	return ix, err
}

// splitFlags pulls -x / --x [value] flags out of args.
func splitFlags(args []string, withValue map[string]bool) (map[string]string, []string) {
	flags := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			k, v, hasEq := strings.Cut(strings.TrimLeft(a, "-"), "=")
			if withValue[k] && !hasEq && i+1 < len(args) {
				i++
				v = args[i]
			}
			flags[k] = v
			continue
		}
		rest = append(rest, a)
	}
	return flags, rest
}

func cmdIndex(args []string) error {
	flags, roots := splitFlags(args, map[string]bool{"exclude": true})
	if len(roots) == 0 {
		roots = defaultRoots()
	}
	excludes := defaultExcludes
	if _, ok := flags["all"]; ok {
		excludes = nil
	}
	if ex := flags["exclude"]; ex != "" {
		excludes = append(append([]string{}, excludes...), strings.Split(ex, ",")...)
	}
	fmt.Fprintf(os.Stderr, "indexing %s\n", strings.Join(roots, ", "))
	var prog atomic.Int64
	done := make(chan struct{})
	start := time.Now()
	tty := isatty.IsTerminal(os.Stderr.Fd())
	go func() {
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if tty {
					fmt.Fprintf(os.Stderr, "\r  %s items…", commas(int(prog.Load())))
				}
			}
		}
	}()
	ix, err := BuildIndex(roots, excludes, &prog)
	close(done)
	if tty {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	if err != nil {
		return err
	}
	walk := time.Since(start)
	if err := ix.Save(indexPath()); err != nil {
		return err
	}
	f, d := ix.Files()
	st, _ := os.Stat(indexPath())
	fmt.Fprintf(os.Stderr, "indexed %s files and %s folders in %s → %s (%s)\n",
		commas(f), commas(d), walk.Round(time.Millisecond), indexPath(), humanSize(st.Size()))
	return nil
}

func cmdFind(args []string) error {
	flags, rest := splitFlags(args, map[string]bool{"n": true, "sort": true})
	limit := 50
	if v, err := strconv.Atoi(flags["n"]); err == nil {
		limit = v
	}
	mode := SortRelevance
	switch flags["sort"] {
	case "newest":
		mode = SortNewest
	case "largest":
		mode = SortLargest
	}
	ix, err := mustLoad()
	if err != nil {
		return err
	}
	q := ParseQuery(strings.Join(rest, " "), time.Now())
	if q.Err != "" {
		return errors.New(q.Err)
	}
	start := time.Now()
	hits, total := Search(ix, q, mode, limit)
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	for _, h := range hits {
		fmt.Fprintln(w, ix.Path(&ix.Entries[h.Idx]))
	}
	fmt.Fprintf(os.Stderr, "%s matches (%d shown) in %s\n", commas(total), len(hits), fmtDur(time.Since(start)))
	return nil
}

func cmdGrep(args []string) error {
	flags, rest := splitFlags(args, map[string]bool{"n": true})
	limit := 500
	if v, err := strconv.Atoi(flags["n"]); err == nil {
		limit = v
	}
	_, re := flags["r"]
	ix, err := mustLoad()
	if err != nil {
		return err
	}
	q := ParseQuery(strings.Join(rest, " "), time.Now())
	if q.Err != "" {
		return errors.New(q.Err)
	}
	pat := q.Text()
	if pat == "" {
		return errors.New("usage: seek grep <text> [filters]")
	}
	color := isatty.IsTerminal(os.Stdout.Fd())
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	var scanned atomic.Int64
	start := time.Now()
	n := 0
	err = Grep(context.Background(), ix, q, pat, re, limit, func(b []ContentHit) {
		for _, h := range b {
			n++
			p := ix.Path(&ix.Entries[h.Idx])
			text := h.Text
			if color {
				end := min(h.Col+h.MLen, len(text))
				text = text[:h.Col] + "\033[1;33m" + text[h.Col:end] + "\033[0m" + text[end:]
				fmt.Fprintf(w, "\033[35m%s\033[0m:\033[32m%d\033[0m: %s\n", p, h.Line, text)
			} else {
				fmt.Fprintf(w, "%s:%d: %s\n", p, h.Line, text)
			}
		}
	}, &scanned)
	fmt.Fprintf(os.Stderr, "%s hits · %s files scanned in %s\n", commas(n), commas(int(scanned.Load())), fmtDur(time.Since(start)))
	return err
}

// cmdWatch keeps the saved index current without the UI, so `seek find`
// and the next UI launch start from fresh data.
func cmdWatch(args []string) error {
	flags, _ := splitFlags(args, nil)
	_, verbose := flags["v"]
	ix := loadOrNil()
	roots, excludes := defaultRoots(), defaultExcludes
	if ix != nil {
		roots, excludes = ix.Roots, ix.Excludes
	}
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
	}
	upd := NewUpdater(ix, roots, excludes)
	var changes, entries atomic.Int64
	upd.OnRebuildStart = func(*atomic.Int64) { logf("rescanning %s ...", strings.Join(roots, ", ")) }
	upd.OnUpdate = func(ix *Index, n int, full bool) {
		entries.Store(int64(len(ix.Entries)))
		switch {
		case full:
			logf("index rebuilt: %s entries", commas(len(ix.Entries)))
		case verbose:
			logf("%d change(s) applied - %s entries", n, commas(len(ix.Entries)))
		default:
			changes.Add(int64(n))
		}
	}
	go func() { // one summary line per minute instead of one per batch
		for range time.Tick(time.Minute) {
			if n := changes.Swap(0); n > 0 {
				logf("%s change(s) in the last minute - %s entries", commas(int(n)), commas(int(entries.Load())))
			}
		}
	}()
	upd.OnError = func(err error) { logf("error: %v", err) }
	upd.Start()
	if upd.Live {
		logf("watching %s for changes (Ctrl+C to stop)", strings.Join(roots, ", "))
	} else {
		logf("live watching isn't available on this OS; rescanning every %s", pollRebuild)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	logf("saving ...")
	upd.Close()
	return nil
}

func cmdStats() error {
	ix, err := mustLoad()
	if err != nil {
		return err
	}
	f, d := ix.Files()
	var total int64
	for i := range ix.Entries {
		total += ix.Entries[i].Size
	}
	st, _ := os.Stat(indexPath())
	fmt.Printf("index     %s (%s)\n", indexPath(), humanSize(st.Size()))
	fmt.Printf("built     %s (%s)\n", ix.Created.Format("2006-01-02 15:04"), humanAge(ix.Created))
	fmt.Printf("roots     %s\n", strings.Join(ix.Roots, ", "))
	fmt.Printf("excludes  %s\n", strings.Join(ix.Excludes, ", "))
	fmt.Printf("files     %s (%s)\n", commas(f), humanSize(total))
	fmt.Printf("folders   %s\n", commas(d))
	return nil
}
