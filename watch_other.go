//go:build !windows

package main

// watchRoots has no efficient recursive equivalent on macOS/Linux without cgo
// or one watch per folder, so the Updater falls back to periodic rescans.
func watchRoots(roots []string, out chan<- string, overflow chan<- struct{}) bool {
	return false
}
