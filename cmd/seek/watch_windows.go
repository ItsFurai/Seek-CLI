//go:build windows

package main

import (
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// watchRoots starts one recursive ReadDirectoryChangesW watch per root.
// Changed paths go to out; if the kernel buffer overflows (or out is full)
// a signal goes to overflow so the caller can rescan.
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
			nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			continue
		}
		started++
		go watchHandle(h, root, out, overflow)
	}
	return started > 0
}

func watchHandle(h windows.Handle, root string, out chan<- string, overflow chan<- struct{}) {
	defer windows.CloseHandle(h)
	const mask = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE
	buf := make([]byte, 256<<10)
	signal := func() {
		select {
		case overflow <- struct{}{}:
		default:
		}
	}
	for {
		var n uint32
		err := windows.ReadDirectoryChanges(h, &buf[0], uint32(len(buf)), true, mask, &n, nil, 0)
		if err != nil {
			if err == windows.ERROR_NOTIFY_ENUM_DIR {
				signal()
				continue
			}
			return // volume gone or handle closed
		}
		if n == 0 { // too many changes to fit in the buffer
			signal()
			continue
		}
		for off := uint32(0); off < n; {
			info := (*windows.FileNotifyInformation)(unsafe.Pointer(&buf[off]))
			name := windows.UTF16ToString(unsafe.Slice(&info.FileName, info.FileNameLength/2))
			select {
			case out <- filepath.Join(root, name):
			default:
				signal() // consumer is behind; a rescan will catch up
			}
			if info.NextEntryOffset == 0 {
				break
			}
			off += info.NextEntryOffset
		}
	}
}
