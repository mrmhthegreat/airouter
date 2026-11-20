// Package clicmd is the importable form of the airouter CLI. Every subcommand
// (ask, speak, transcribe, serve, provider, tenant, usage, presets, free-pool,
// bootstrap, ollama, me) is an exported function that takes the same []string
// args the binary would — so any Go program can call them directly without
// spawning a process or duplicating the flag-parsing logic.
//
//	airouter provider list          // from the standalone binary
//	clicmd.Provider([]string{"list"})   // from inside any Go program
//
// Both call the same code, the same *airouter.Client methods, the same DB.
//
// Use Dispatch(args) for the full top-level routing — it picks the
// subcommand from args[0] and calls the matching function with args[1:].
// Use the individual functions when you want to call one subcommand
// directly from your own dispatcher.
package clicmd

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// UsageText is the top-level help. Exported so embedders can show it from
// their own -h / --help handlers.
const UsageText = `airouter — route prompts across free AI providers (Groq, Gemini, Ollama,
any OpenAI-compatible URL). Auto-switches when one hits a limit.
Supports text chat, text-to-speech (TTS) and speech-to-text (STT).

  ask [flags] PROMPT...                send a prompt (or pipe it on stdin)
  speak [flags] TEXT                   text → audio (WAV bytes; use -o to save to a file)
  transcribe [flags] FILE              audio file → text
  serve [-addr :8080]                  run the HTTP API standalone
  provider add|list|rm|enable|disable|reset|set-key|limits|import   (add -tenant ID to act for one user)
  ollama list|add [-tenant ID] MODEL...
  tenant add|list|rotate
  me [-url U] [-key K] list|catalog|add|pick|ollama|limits|set-key|import|export|usage ...
                                       self-service for one user, over HTTP with their own key
  bootstrap [-preset NAME] [-category text|tts|stt|all] [-store-keys] [-tenant ID]
                                       seed the DB from the curated free-pool catalog
  usage [-tenant ID] [-day YYYY-MM-DD]
  presets                              list built-in provider presets
  free-pool [-category X]              list the curated free-tier model catalog

Database: -db FILE or $AIROUTER_DB (default ./airouter.db). Provider keys are stored
          encrypted; the secret is $AIROUTER_SECRET or <db>.key (back it up separately).
Output:   defaults to text when stdout is a pipe, pretty when interactive;
          -json / -envelope / -text override. Run '<command> -h' for flags.`

// Dispatch is the top-level entry point. Given the same args the standalone
// `airouter` binary would receive (e.g. ["provider", "list"]), it picks the
// right subcommand and calls it. Returns the subcommand's error (if any) so
// the caller can decide how to report it.
//
// Use this from your own main() to embed the full airouter CLI:
//
//	clicmd.Dispatch(os.Args[1:])
//
// Or call the individual functions (Provider, Bootstrap, Presets, FreePool,
// Ask, Speak, Transcribe, Serve, Ollama, Tenant, Usage, Me) directly when
// you want a specific subcommand from your own dispatcher.
func Dispatch(args []string) error {
	if len(args) == 0 {
		fmt.Println(UsageText)
		os.Exit(2)
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "ask":
		err = Ask(rest)
	case "speak":
		err = Speak(rest)
	case "transcribe":
		err = Transcribe(rest)
	case "serve":
		err = Serve(rest)
	case "ollama":
		err = Ollama(rest)
	case "provider":
		err = Provider(rest)
	case "tenant":
		err = Tenant(rest)
	case "usage":
		err = Usage(rest)
	case "presets":
		err = Presets(rest)
	case "free-pool":
		err = FreePool(rest)
	case "bootstrap":
		err = Bootstrap(rest)
	case "me":
		err = Me(rest)
	case "-h", "--help", "help":
		fmt.Println(UsageText)
	default:
		fmt.Println(UsageText)
		os.Exit(2)
	}
	if err != nil {
		PrintError(err)
		os.Exit(1)
	}
	return nil
}

// PrintError turns router errors into helpful stderr output. The error types
// carry useful structured info (which providers, why, when to retry) that we
// surface here. Exported so embedders can use the same formatting.
func PrintError(err error) {
	var ex *types.ExhaustedError
	var af *types.AllFailedError
	var tl *types.TenantLimitError
	switch {
	case errors.As(err, &ex):
		fmt.Fprintf(os.Stderr, "error: all providers are limited right now. Retry in %s\n", ex.RetryAfter.Round(time.Second))
		for n, why := range ex.Reasons {
			fmt.Fprintf(os.Stderr, "  - %s: %s\n", n, why)
		}
	case errors.As(err, &af):
		fmt.Fprintln(os.Stderr, "error: every provider failed")
		for _, a := range af.Attempts {
			fmt.Fprintf(os.Stderr, "  - %s: %s\n", a.Provider, a.Error)
		}
	case errors.As(err, &tl):
		fmt.Fprintf(os.Stderr, "error: tenant daily limit reached; resets in %s\n", tl.RetryAfter.Round(time.Minute))
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
	}
}
