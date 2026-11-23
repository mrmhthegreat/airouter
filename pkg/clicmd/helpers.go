package clicmd

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/sqlopen"
)

// DbFlag registers the standard -db flag. Default is $AIROUTER_DB or ./airouter.db.
// Exported so embedders can register the same flag in their own FlagSet.
func DbFlag(fs *flag.FlagSet) *string {
	def := os.Getenv("AIROUTER_DB")
	if def == "" {
		def = "airouter.db"
	}
	return fs.String("db", def, "SQLite database file")
}

// OpenRouter opens the SQLite store (provider keys encrypted at rest) and
// constructs a Router wired up with a stderr logger. Used by every
// subcommand. Exported so embedders can build the same Router.
func OpenRouter(path string, policy ...router.TenantPolicy) (*router.Router, error) {
	logf := func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[airouter] "+f+"\n", a...) }
	st, _, err := sqlopen.Store(path, logf)
	if err != nil {
		return nil, err
	}
	o := router.Options{Store: st, Logf: logf}
	if len(policy) > 0 {
		o.TenantPolicy = policy[0]
	}
	return router.New(o)
}

// ReadSecret reads a secret without echoing it into shell history: from the
// -key flag value if given, else one line from stdin. Exported for embedders.
func ReadSecret(flagVal, what string) (string, error) {
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

// ReadPromptFromStdinOrArgs returns the prompt from joined args, or from stdin
// when args are empty or "-". Empty result means the user gave nothing.
func ReadPromptFromStdinOrArgs(args []string) (string, error) {
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

// EnvOr returns os.Getenv(k) when set, else def.
func EnvOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// StdoutIsTerminal reports whether os.Stdout is a character device (a TTY).
func StdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode().Type() == fs.ModeCharDevice
}

// JoinSpace joins args with single spaces — kept as a helper for the speak subcommand.
func JoinSpace(parts []string) string {
	return strings.Join(parts, " ")
}

// FmtDur turns a number of seconds into "1h05m" / "0h02m" style strings.
func FmtDur(sec int64) string { return fmt.Sprintf("%dh%02dm", sec/3600, sec%3600/60) }

// FormatLimit turns an int limit into a human-readable cell value: 0 becomes "—".
func FormatLimit(n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprint(n)
}

// FormatCategoriesLabel pretty-prints the categories list for the stderr banner.
// Mirrors the BootstrapProviders convention: nil = default text, empty = all.
func FormatCategoriesLabel(cats []string) string {
	if cats == nil {
		return "text (default)"
	}
	if len(cats) == 0 {
		return "all"
	}
	return strings.Join(cats, ", ")
}
