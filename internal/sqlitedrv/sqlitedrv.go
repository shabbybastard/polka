// Package sqlitedrv selects the SQLite driver per platform. Desktop and
// server builds use the pure-Go modernc driver. Android is not supported by
// this build: modernc/libc relies on system calls that Android seccomp
// policies reject, and the CGO mattn driver is not shipped.
package sqlitedrv

// Options are common connection settings independent of the driver.
type Options struct {
	WAL         bool
	SyncNormal  bool
	BusyTimeout int // milliseconds
	ForeignKeys bool
}
