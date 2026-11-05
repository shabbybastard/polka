//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package server

// diskSpace is unavailable on this platform; /stats simply omits usage.
func diskSpace(string) (total, free int64, ok bool) {
	return 0, 0, false
}
