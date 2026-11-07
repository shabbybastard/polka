//go:build !android

package sqlitedrv

import (
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

const driverName = "sqlite"

func dsn(path string, o Options) string {
	var p []string
	if o.WAL {
		p = append(p, "_pragma=journal_mode(WAL)")
	}
	if o.SyncNormal {
		p = append(p, "_pragma=synchronous(NORMAL)")
	}
	if o.BusyTimeout > 0 {
		p = append(p, fmt.Sprintf("_pragma=busy_timeout(%d)", o.BusyTimeout))
	}
	if o.ForeignKeys {
		p = append(p, "_pragma=foreign_keys(ON)")
	}
	// Build a "file:" URI. Only the characters that SQLite's URI parser
	// treats specially must be percent-escaped; a Windows drive-letter path
	// or a UNC path must keep its original form (backslashes and all) for
	// the parser to accept it. Building the URI via url.URL is wrong here:
	// it would move a Windows path into the URI authority
	// ("file://C:%5CUsers%5C..."), which SQLite rejects.
	return "file:" + escapeDSNPath(path) + "?" + strings.Join(p, "&")
}

// escapeDSNPath percent-encodes characters that would otherwise terminate
// the path part of a SQLite URI. '%' is escaped first so an existing name
// with hex-looking text round-trips through the parser's %XX decoding.
func escapeDSNPath(p string) string {
	p = strings.ReplaceAll(p, "%", "%25")
	p = strings.ReplaceAll(p, "?", "%3F")
	p = strings.ReplaceAll(p, "#", "%23")
	return p
}
