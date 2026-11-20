# airouter — with importable CLI (`pkg/clicmd`)

This is the airouter source tree with one refactor on top of upstream: the
CLI subcommand logic that used to live in `cmd/airouter/*.go` (package `main`,
not importable) has been moved into a new package, **`pkg/clicmd/`**, so any
Go program importing airouter can call the same subcommands as Go functions.

## What changed vs upstream airouter

### Added: `pkg/clicmd/` package

Every CLI subcommand is now an exported function:

| Function | Args | Calls |
|---|---|---|
| `clicmd.Dispatch(args)` | `["provider", "list"]` etc. | routes to the function below |
| `clicmd.Provider(args)` | `["list"]`, `["add", "-preset", "groq", "-key", "..."]`, `["rm", "5"]`, `["enable", "5"]`, `["disable", "5"]`, `["reset", "5"]`, `["set-key", "5", "-key", "..."]`, `["limits", "5", "-rpm", "30"]`, `["import", "providers.json"]` | `ac.SaveProvider`, `ac.DeleteProvider`, `ac.SetDisabled`, `ac.ClearPause`, `ac.SetProviderKey`, `ac.UpdateTenantLimits`, `ac.ImportTenantProviders`, `ac.Status` |
| `clicmd.Bootstrap(args)` | `["-overwrite"]`, `["-preset", "groq"]`, `["-category", "text,tts"]`, `["-store-keys"]`, `["-tenant", "ID"]` | `ac.BootstrapProvidersOpts` |
| `clicmd.Presets(args)` | (none) | `preset.Definitions` |
| `clicmd.FreePool(args)` | `["-category", "text"]`, `["-json"]` | `preset.FreePoolByCategory` |
| `clicmd.Tenant(args)` | `["add", "acme"]`, `["list"]`, `["rotate", "acme"]` | `ac.CreateTenant`, `ac.RotateTenantKey`, `ac.Store().ListTenants` |
| `clicmd.Usage(args)` | `["-tenant", "ID"]`, `["-day", "YYYY-MM-DD"]` | `ac.Usage` |
| `clicmd.Ask(args)` | `PROMPT...`, `[-system S]`, `[-json]`, `[-envelope]`, `[-provider NAME]` | `ac.Generate` |
| `clicmd.Speak(args)` | `TEXT`, `[-voice V]`, `[-format wav,mp3,pcm]`, `[-o FILE]` | `ac.Speak` |
| `clicmd.Transcribe(args)` | `FILE`, `[-language L]`, `[-provider NAME]` | `ac.Transcribe` |
| `clicmd.Ollama(args)` | `["list"]`, `["add", "MODEL..."]` | `ac.OllamaModels`, `ac.AddTenantOllama` |
| `clicmd.Serve(args)` | `[-addr :8080]`, `[-cors *]` | runs `ac.Handler` standalone |
| `clicmd.Me(args)` | `[-url U] [-key K] <subcommand>` | tenant self-service over HTTP |

Plus exported helpers: `DbFlag`, `OpenRouter`, `ReadSecret`,
`ReadPromptFromStdinOrArgs`, `EnvOr`, `StdoutIsTerminal`, `JoinSpace`,
`FmtDur`, `FormatLimit`, `FormatCategoriesLabel`, `Pretty`, `PrintMine`,
`PrintError`, `UsageText`, `MeUsageText`.

### Slimmed: `cmd/airouter/main.go`

Was 108 lines with the dispatch table + error printer. Now 14 lines:

```go
package main

import (
	"os"

	"github.com/mrmhthegreat/airouter/pkg/clicmd"
)

func main() {
	clicmd.Dispatch(os.Args[1:])
}
```

### Deleted: `cmd/airouter/{helpers,ask,speak,transcribe,tenant,presets,provider,bootstrap,ollama,ollama_shared,me}.go`

All moved to `pkg/clicmd/` with capitalized function names. No logic was
changed — only the package name and the function capitalization.

## Build

```sh
go build -o airouter ./cmd/airouter
./airouter                      # top-level usage
./airouter provider list
./airouter presets
./airouter free-pool -category text
GROQ_API_KEY=gsk_... ./airouter bootstrap -store-keys
./airouter provider add -preset gemini -key AIza... -model gemini-2.5-flash
./airouter serve -addr :8080
```

The standalone binary behaves identically to upstream airouter — same
commands, same flags, same output.

## Import from any Go program

```go
package main

import (
	"os"
	"github.com/mrmhthegreat/airouter/pkg/clicmd"
)

func main() {
	clicmd.Dispatch(os.Args[1:])   // full top-level routing
	// or call one subcommand directly:
	// clicmd.Provider([]string{"list"})
	// clicmd.Bootstrap([]string{"-store-keys"})
	// clicmd.Ask([]string{"What is 2+2?"})
}
```

## Tests

```sh
go test ./pkg/...
```

All upstream tests still pass — the refactor only moved code, it didn't change
any logic.

## Why this refactor

Before: any Go program that wanted to embed airouter's CLI subcommands had to
either (a) shell out to the `airouter` binary, or (b) duplicate the flag
parsing + dispatch logic. The duplication was the painful part — every
subcommand's flags had to be re-implemented.

After: `clicmd.Provider(args)`, `clicmd.Bootstrap(args)`, etc. are just
function calls. Any Go program importing airouter gets the full CLI surface
for free, and bug fixes / new subcommands added to airouter's CLI
automatically show up in every embedder.

## Files

```
pkg/clicmd/
├── dispatch.go        Dispatch(args) + UsageText + PrintError
├── helpers.go         DbFlag, OpenRouter, ReadSecret, ReadPromptFromStdinOrArgs,
│                      EnvOr, StdoutIsTerminal, JoinSpace, FmtDur, FormatLimit,
│                      FormatCategoriesLabel
├── provider.go        Provider(args) — add/list/rm/enable/disable/reset/set-key/limits/import
├── bootstrap.go       Bootstrap(args)
├── presets.go         Presets(args), FreePool(args)
├── tenant.go          Tenant(args), Usage(args)
├── ask.go             Ask(args), Serve(args)
├── speak.go           Speak(args)
├── transcribe.go      Transcribe(args)
├── ollama.go          Ollama(args), AddSharedOllama
└── me.go              Me(args), meCLI.Call, Pretty, PrintMine
```
