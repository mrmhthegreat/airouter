// Package main is the airouter CLI entry point. Subcommands are dispatched to
// files in this package: ask.go, serve.go, provider.go, tenant.go, usage.go,
// presets.go, bootstrap.go. Driver selection (CGO mattn vs pure-Go modernc) is
// in driver_cgo.go / driver_pure.go.
package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

const usageText = `airouter — route prompts across free AI providers (Groq, Gemini, Ollama,
any OpenAI-compatible URL). Auto-switches when one hits a limit.
Supports text chat, text-to-speech (TTS) and speech-to-text (STT).

  airouter ask [flags] PROMPT...       send a prompt (or pipe it on stdin)
  airouter speak [flags] TEXT          text → audio (WAV bytes; use -o to save to a file)
  airouter transcribe [flags] FILE     audio file → text
  airouter serve [-addr :8080]         run the HTTP API
  airouter provider add|list|rm|enable|disable|reset|set-key|import
  airouter tenant add|list|rotate
  airouter bootstrap [-preset NAME] [-category text|tts|stt|all] [-store-keys]
                                       seed the DB from the curated free-pool catalog
  airouter usage [-tenant ID] [-day YYYY-MM-DD]
  airouter presets                     list built-in provider presets
  airouter free-pool [-category X]     list the curated free-tier model catalog

Database: -db FILE or $AIROUTER_DB (default ./airouter.db). Provider keys are stored
          encrypted; the secret is $AIROUTER_SECRET or <db>.key (back it up separately).
Output:   defaults to text when stdout is a pipe, pretty when interactive;
          -json / -envelope / -text override. Run 'airouter <command> -h' for flags.`

func main() {
	if len(os.Args) < 2 {
		fmt.Println(usageText)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "ask":
		err = cmdAsk(args)
	case "speak":
		err = cmdSpeak(args)
	case "transcribe":
		err = cmdTranscribe(args)
	case "serve":
		err = cmdServe(args)
	case "provider":
		err = cmdProvider(args)
	case "tenant":
		err = cmdTenant(args)
	case "usage":
		err = cmdUsage(args)
	case "presets":
		err = cmdPresets(args)
	case "free-pool":
		err = cmdFreePool(args)
	case "bootstrap":
		err = cmdBootstrap(args)
	case "-h", "--help", "help":
		fmt.Println(usageText)
	default:
		fmt.Println(usageText)
		os.Exit(2)
	}
	if err != nil {
		printError(err)
		os.Exit(1)
	}
}

// printError turns router errors into helpful stderr output. The error types
// carry useful structured info (which providers, why, when to retry) that we
// surface here.
func printError(err error) {
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
