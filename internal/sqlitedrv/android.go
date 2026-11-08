//go:build android

package sqlitedrv

import (
	"database/sql"
	"errors"
)

// Open reports an explicit error instead of failing to compile with an
// undefined driverName. Android support would need a CGO SQLite driver.
func Open(path string, o Options) (*sql.DB, error) {
	return nil, errors.New("sqlitedrv: Android builds are not supported")
}
