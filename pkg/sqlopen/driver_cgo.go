//go:build cgo

package sqlopen

import _ "github.com/mattn/go-sqlite3"

// driverName is the name to pass to sql.Open. When CGO is enabled we use
// mattn/go-sqlite3, which is the fastest and most battle-tested SQLite driver
// for Go but requires a C compiler at build time.
const driverName = "sqlite3"

// dsn returns the connection string for the given path. mattn uses libsqlite3
// query-string-style pragmas.
func dsn(path string) string {
	return "file:" + path + "?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on"
}
