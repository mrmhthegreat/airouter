//go:build !cgo

package main

import _ "modernc.org/sqlite"

// sqlDriver is the name to pass to sql.Open. When CGO is disabled we use
// modernc.org/sqlite — a pure-Go (transpiled-from-C) SQLite that needs no C
// compiler. Slightly slower than mattn but builds anywhere Go does, including
// Windows without MinGW.
const sqlDriver = "sqlite"

// dsn returns the connection string for the given path. modernc uses
// `_pragma=...` query params (one per pragma).
func dsn(path string) string {
	return "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
}
