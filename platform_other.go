//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func defaultRoots() []string {
	if h, err := os.UserHomeDir(); err == nil {
		return []string{h}
	}
	return []string{"/"}
}

func opener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

func openPath(path string) error { return exec.Command(opener(), path).Start() }

func revealPath(path string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-R", path).Start()
	}
	return exec.Command(opener(), filepath.Dir(path)).Start()
}
