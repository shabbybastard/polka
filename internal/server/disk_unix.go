//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package server

import "syscall"

// diskSpace reports total and available bytes for the filesystem holding
// path. The Telegram /stats command uses it; other platforms return false.
func diskSpace(path string) (total, free int64, ok bool) {
	if path == "" {
		return 0, 0, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bsize := int64(st.Bsize)
	if bsize <= 0 {
		return 0, 0, false
	}
	return int64(st.Blocks) * bsize, int64(st.Bavail) * bsize, true
}
