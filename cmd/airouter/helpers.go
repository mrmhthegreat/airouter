package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/sqlopen"
)

// dbFlag registers the standard -db flag. Default is $AIROUTER_DB or ./airouter.db.
func dbFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("AIROUTER_DB")
	if def == "" {
		def = "airouter.db"
	}
	return fs.String("db", def, "SQLite database file")
}

// openRouter opens the SQLite store (provider keys encrypted at rest) and
// constructs a Router wired up with a stderr logger. Used by every subcommand.
func openRouter(path string) (*router.Router, error) {
	logf := func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[airouter] "+f+"\n", a...) }
	st, _, err := sqlopen.Store(path, logf)
	if err != nil {
		return nil, err
	}
	return router.New(router.Options{Store: st, Logf: logf})
}

// readSecret reads a secret without echoing it into shell history: from the
// -key flag value if given, else one line from stdin.
func readSecret(flagVal, what string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	fmt.Fprintf(os.Stderr, "%s (input is not hidden; paste and press Enter): ", what)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no key given")
	}
	return strings.TrimSpace(line), nil
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
