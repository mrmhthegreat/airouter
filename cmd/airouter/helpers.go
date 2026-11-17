package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/store"
)

// dbFlag registers the standard -db flag. Default is $AIROUTER_DB or ./airouter.db.
func dbFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("AIROUTER_DB")
	if def == "" {
		def = "airouter.db"
	}
	return fs.String("db", def, "SQLite database file")
}

// openRouter opens the SQLite store and constructs a Router wired up with a
// stderr logger. Used by every CLI subcommand.
func openRouter(path string) (*router.Router, error) {
	db, err := sql.Open(sqlDriver, dsn(path))
	if err != nil {
		return nil, err
	}
	st, err := store.NewSQLStore(db)
	if err != nil {
		return nil, err
	}
	return router.New(router.Options{
		Store: st,
		Logf:  func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[airouter] "+f+"\n", a...) },
	})
}

// readPromptFromStdinOrArgs returns the prompt from joined args, or from stdin
// when args are empty or "-". Empty result means the user gave nothing.
func readPromptFromStdinOrArgs(args []string) (string, error) {
	prompt := strings.Join(args, " ")
	if prompt == "" || prompt == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		prompt = string(b)
	}
	return strings.TrimSpace(prompt), nil
}
