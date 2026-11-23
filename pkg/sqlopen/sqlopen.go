// Package sqlopen opens airouter's SQLite database with the right driver
// (mattn/go-sqlite3 when CGO is on, pure-Go modernc.org/sqlite otherwise) and
// wires provider-key encryption. Import it only if you want SQLite; the rest
// of airouter works with any store.Store.
package sqlopen

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/mrmhthegreat/airouter/pkg/store"
)

// DB opens (creating if needed) the SQLite file at path.
func DB(path string) (*sql.DB, error) { return sql.Open(driverName, dsn(path)) }

// Store opens the database at path as a store.Store whose provider API keys
// are encrypted at rest. The secret is $AIROUTER_SECRET, or a key file at
// path+".key" (mode 0600, created on first use). Keys written by older
// versions in plaintext are encrypted on first open. source says where the
// secret came from (safe to log).
func Store(path string, logf func(string, ...any)) (st store.Store, source string, err error) {
	db, err := DB(path)
	if err != nil {
		return nil, "", err
	}
	base, err := store.NewSQLStore(db)
	if err != nil {
		return nil, "", err
	}
	secret, source, err := store.MasterSecret(path + ".key")
	if err != nil {
		return nil, "", err
	}
	enc, err := store.WithEncryption(base, secret)
	if err != nil {
		return nil, "", err
	}
	n, err := enc.EncryptExisting(contextBackground())
	if err != nil {
		return nil, "", fmt.Errorf("encrypting stored provider keys: %w", err)
	}
	if n > 0 && logf != nil {
		logf("encrypted %d provider key(s) that were stored in plaintext", n)
	}
	return enc, source, nil
}

func contextBackground() context.Context { return context.Background() }
