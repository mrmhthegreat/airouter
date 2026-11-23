package clicmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// MeUsageText is the help for the `me` subcommand.
const MeUsageText = `usage: me [-url URL] [-key KEY] <command>

  whoami                                 who am I
  list                                   my models (+ shared ones, read-only)
  catalog [-preset NAME]                 providers and models I can pick from
  add -preset P -model M [-key-stdin] [-rpm N -rpd N -tpm N -workers N]
  pick -preset P [-key-stdin] MODEL...   add several catalog models with one key
  ollama [add MODEL...]                  list server Ollama models, or add some
  limits ID [-rpm N -rpd N -tpm N -workers N -priority N]
  set-key ID [-key-stdin]                replace a model's API key
  enable|disable|reset|rm ID
  import FILE.json | export              JSON file in / out
  usage [-day YYYY-MM-DD]                my usage
  update [-name X] [-tz Z]               my profile
  rotate-key                             new API key (the old one stops working)

flags (before the command):
  -url  server (default $AIROUTER_URL or http://localhost:8080)
  -key  my tenant key (default $AIROUTER_KEY)`

// Me is the self-service CLI for ONE tenant. It talks to a running
// server over HTTP with the tenant's own API key (-key or $AIROUTER_KEY) and
// never opens the local database, so it works against any airouter server.
//
// (The operator's CLI, run on the server itself, is `provider -tenant ID`.)
//
// Args (flags first, then the subcommand):
//
//	["-url", "http://localhost:8080", "-key", "air_...", "list"]
//	["whoami"]
//	["catalog", "-preset", "groq"]
//	["add", "-preset", "groq", "-model", "openai/gpt-oss-120b", "-key-stdin"]
//	["pick", "-preset", "groq", "-key-stdin", "openai/gpt-oss-120b", "openai/gpt-oss-20b"]
//	["limits", "5", "-rpm", "30"]
//	["set-key", "5", "-key-stdin"]
//	["enable", "5"] / ["disable", "5"] / ["reset", "5"] / ["rm", "5"]
//	["import", "providers.json"] / ["export"]
//	["usage"] / ["usage", "-day", "2026-01-15"]
//	["update", "-name", "Acme Inc.", "-tz", "America/Los_Angeles"]
//	["rotate-key"]
func Me(args []string) error {
	root := flag.NewFlagSet("me", flag.ContinueOnError)
	url := root.String("url", EnvOr("AIROUTER_URL", "http://localhost:8080"), "server url")
	key := root.String("key", os.Getenv("AIROUTER_KEY"), "tenant api key")
	if err := root.Parse(args); err != nil || root.NArg() == 0 {
		return errors.New(MeUsageText)
	}
	if *key == "" {
		return errors.New("missing API key: set $AIROUTER_KEY or pass -key air_...")
	}
	c := meCLI{*url, *key}
	cmd, rest := root.Arg(0), root.Args()[1:]

	fs := flag.NewFlagSet("me "+cmd, flag.ContinueOnError)
	var (
		presetName, model, name, tz, day string
		rpm, rpd, tpm, workers, prio     int
		keyStdin                         bool
	)
	fs.StringVar(&presetName, "preset", "", "preset name")
	fs.StringVar(&model, "model", "", "model id")
	fs.StringVar(&name, "name", "", "name")
	fs.StringVar(&tz, "tz", "", "timezone")
	fs.StringVar(&day, "day", "", "day")
	fs.IntVar(&rpm, "rpm", -1, "requests per minute")
	fs.IntVar(&rpd, "rpd", -1, "requests per day")
	fs.IntVar(&tpm, "tpm", -1, "tokens per minute")
	fs.IntVar(&workers, "workers", -1, "parallel requests")
	fs.IntVar(&prio, "priority", -1, "priority (lower = first)")
	fs.BoolVar(&keyStdin, "key-stdin", false, "read the provider key from stdin")
	// allow `ID -rpm 5` as well as `-rpm 5 ID`
	var pos []string
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		rest = fs.Args()
		if len(rest) > 0 {
			pos, rest = append(pos, rest[0]), rest[1:]
		}
	}
	providerKey := func() (string, error) {
		if !keyStdin {
			return "", nil
		}
		return ReadSecret("", "Provider API key")
	}
	idArg := func() (string, error) {
		if len(pos) != 1 {
			return "", errors.New("expected one model id (see `me list`)")
		}
		if _, err := strconv.ParseInt(pos[0], 10, 64); err != nil {
			return "", errors.New("model id must be a number")
		}
		return pos[0], nil
	}
	limits := func() map[string]any {
		m := map[string]any{}
		for k, v := range map[string]int{"rpm": rpm, "rpd": rpd, "tpm": tpm, "workers": workers, "priority": prio} {
			if v >= 0 {
				m[k] = v
			}
		}
		return m
	}

	var out []byte
	var err error
	switch cmd {
	case "whoami":
		out, err = c.call("GET", "/v1/me", nil)
	case "list":
		out, err = c.call("GET", "/v1/me/providers", nil)
		if err == nil {
			return PrintMine(out)
		}
	case "catalog":
		p := ""
		if presetName != "" {
			p = "?preset=" + presetName
		}
		out, err = c.call("GET", "/v1/me/catalog"+p, nil)
	case "add":
		k, e := providerKey()
		if e != nil {
			return e
		}
		body := limits()
		body["preset"], body["model"] = presetName, model
		if k != "" {
			body["api_key"] = k
		}
		if name != "" {
			body["name"] = name
		}
		out, err = c.call("POST", "/v1/me/providers", body)
	case "pick":
		k, e := providerKey()
		if e != nil {
			return e
		}
		out, err = c.call("POST", "/v1/me/catalog/add", map[string]any{"preset": presetName, "api_key": k, "models": pos})
	case "ollama":
		if len(pos) > 0 && pos[0] == "add" {
			out, err = c.call("POST", "/v1/me/ollama/add", map[string]any{"models": pos[1:]})
		} else {
			out, err = c.call("GET", "/v1/me/ollama", nil)
		}
	case "limits":
		id, e := idArg()
		if e != nil {
			return e
		}
		out, err = c.call("POST", "/v1/me/providers/"+id+"/limits", limits())
	case "set-key":
		id, e := idArg()
		if e != nil {
			return e
		}
		k, e := ReadSecret("", "New provider API key")
		if e != nil {
			return e
		}
		out, err = c.call("POST", "/v1/me/providers/"+id+"/key", map[string]string{"api_key": k})
	case "enable", "disable", "reset":
		id, e := idArg()
		if e != nil {
			return e
		}
		out, err = c.call("POST", "/v1/me/providers/"+id+"/"+cmd, nil)
	case "rm":
		id, e := idArg()
		if e != nil {
			return e
		}
		out, err = c.call("DELETE", "/v1/me/providers/"+id, nil)
	case "import":
		if len(pos) != 1 {
			return errors.New("usage: me import FILE.json")
		}
		raw, e := os.ReadFile(pos[0])
		if e != nil {
			return e
		}
		var v any
		if e := json.Unmarshal(raw, &v); e != nil {
			return fmt.Errorf("%s is not valid JSON: %w", pos[0], e)
		}
		if arr, ok := v.([]any); ok {
			v = map[string]any{"providers": arr}
		}
		out, err = c.call("POST", "/v1/me/import", v)
	case "export":
		out, err = c.call("GET", "/v1/me/export", nil)
	case "usage":
		p := ""
		if day != "" {
			p = "?day=" + day
		}
		out, err = c.call("GET", "/v1/me/usage"+p, nil)
	case "update":
		out, err = c.call("PATCH", "/v1/me", map[string]string{"name": name, "tz": tz})
	case "rotate-key":
		out, err = c.call("POST", "/v1/me/rotate-key", nil)
	default:
		return errors.New(MeUsageText)
	}
	if err != nil {
		return err
	}
	Pretty(out)
	return nil
}

// meCLI is the HTTP client used by the Me subcommand. Exported so embedders
// can build their own tenant self-service tools against any airouter server.
type meCLI struct{ base, key string }

// Call sends one HTTP request to the airouter server with the tenant's API key.
// Exported for embedders that want the same HTTP shape.
func (c meCLI) Call(method, path string, in any) ([]byte, error) { return c.call(method, path, in) }

func (c meCLI) call(method, path string, in any) ([]byte, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.base, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, e.Error)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// Pretty pretty-prints JSON bytes, or falls back to raw text. Exported for embedders.
func Pretty(b []byte) {
	var v any
	if json.Unmarshal(b, &v) == nil {
		if o, err := json.MarshalIndent(v, "", "  "); err == nil {
			fmt.Println(string(o))
			return
		}
	}
	fmt.Println(string(b))
}

// PrintMine prints the "me list" response as a table.
func PrintMine(b []byte) error {
	// field names differ in case/underscores, so decode loosely
	var raw struct {
		Own    []map[string]any `json:"own"`
		Shared []map[string]any `json:"shared"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		Pretty(b)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tMODEL\tSTATE\tUSED\tRPM\tRPD\tTPM\tKEY")
	g := func(m map[string]any, k string) string {
		if v, ok := m[k]; ok && v != nil {
			if f, ok := v.(float64); ok {
				return strconv.FormatInt(int64(f), 10)
			}
			return fmt.Sprint(v)
		}
		return "-"
	}
	for _, m := range raw.Own {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", g(m, "id"), g(m, "name"), g(m, "model"), g(m, "state"),
			g(m, "used_today"), g(m, "rpm"), g(m, "rpd"), g(m, "tpm"), g(m, "key_hint"))
	}
	tw.Flush()
	if len(raw.Own) == 0 {
		fmt.Println("(you have no models of your own yet: try `me catalog` or `me pick`)")
	}
	if len(raw.Shared) > 0 {
		fmt.Printf("\n%d shared model(s) from the server also serve your requests.\n", len(raw.Shared))
	}
	return nil
}
