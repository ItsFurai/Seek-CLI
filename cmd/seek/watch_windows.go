//go:build windows

package main

import (
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const watchMask = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
	windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE

// dirWatch is one recursive ReadDirectoryChangesW watch using overlapped I/O.
type dirWatch struct {
	h    windows.Handle
	root string
	buf  []byte
	ov   windows.Overlapped
}

// issue queues the next read. Windows only records changes while a read is
// queued, so the first one must be issued before watchRoots returns.
func (w *dirWatch) issue() error {
	err := windows.ReadDirectoryChanges(w.h, &w.buf[0], uint32(len(w.buf)), true, watchMask, nil, &w.ov, 0)
	if err == windows.ERROR_IO_PENDING {
		return nil
	}
	return err
}

// watchRoots starts one recursive watch per root. Changed paths go to out;
// if the kernel buffer overflows (or out is full) a signal goes to overflow
// so the caller can rescan. Watching has begun by the time it returns.
func watchRoots(roots []string, out chan<- string, overflow chan<- struct{}) bool {
	started := 0
	for _, root := range roots {
		root, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		p, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
		if err != nil {
			continue
		}
		ev, err := windows.CreateEvent(nil, 1, 0, nil)
		if err != nil {
			windows.CloseHandle(h)
			continue
		}
		w := &dirWatch{h: h, root: root, buf: make([]byte, 256<<10)}
		w.ov.HEvent = ev
		if err := w.issue(); err != nil {
			windows.CloseHandle(ev)
			windows.CloseHandle(h)
			continue
		}
		started++
		go w.loop(out, overflow)
	}
	return started > 0
}

func (w *dirWatch) loop(out chan<- string, overflow chan<- struct{}) {
	defer windows.CloseHandle(w.h)
	defer windows.CloseHandle(w.ov.HEvent)
	signal := func() {
		select {
		case overflow <- struct{}{}:
		default:
		}
	}
	for {
		var n uint32
		err := windows.GetOverlappedResult(w.h, &w.ov, &n, true)
		switch {
		case err == windows.ERROR_NOTIFY_ENUM_DIR:
			signal()
		case err != nil:
			return // volume gone or handle closed
		case n == 0: // too many changes to fit in the buffer
			signal()
		default:
			w.dispatch(n, out, signal)
		}
		windows.ResetEvent(w.ov.HEvent)
		if w.issue() != nil {
			return
		}
	}
}

func (w *dirWatch) dispatch(n uint32, out chan<- string, signal func()) {
	for off := uint32(0); off < n; {
		info := (*windows.FileNotifyInformation)(unsafe.Pointer(&w.buf[off]))
		name := windows.UTF16ToString(unsafe.Slice(&info.FileName, info.FileNameLength/2))
		select {
		case out <- filepath.Join(w.root, name):
		default:
			signal() // consumer is behind; a rescan will catch up
		}
		if info.NextEntryOffset == 0 {
			return
		}
		off += info.NextEntryOffset
	}
}
