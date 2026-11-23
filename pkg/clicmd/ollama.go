package clicmd

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Ollama implements `ollama list` and `ollama add [-tenant ID] MODEL...`.
// Models are installed on the local Ollama server ($OLLAMA_HOST or
// http://localhost:11434).
//
// Args:
//
//	["list"]                       → OllamaModels — what's installed locally
//	["add", "gemma3:4b"]           → addSharedOllama — shared provider for this model
//	["add", "-tenant", "acme", "gemma3:4b", "qwen2.5:3b"]  → AddTenantOllama — private to one tenant
func Ollama(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: ollama list | ollama add [-tenant ID] MODEL...")
	}
	sub := args[0]
	fs := flag.NewFlagSet("ollama "+sub, flag.ExitOnError)
	db := DbFlag(fs)
	tenant := fs.String("tenant", "", "add the models as private to this tenant (default: shared by all)")
	_ = fs.Parse(args[1:])
	r, err := OpenRouter(*db, router.TenantPolicy{AllowCustomBaseURL: true})
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch sub {
	case "list":
		ms, err := r.OllamaModels(ctx)
		if err != nil {
			return err
		}
		for _, m := range ms {
			fmt.Printf("%-30s %6s  %s GB\n", m.Name, m.Params, m.SizeGB)
		}
		if len(ms) == 0 {
			fmt.Println("no models installed; try: ollama pull gemma3:4b")
		}
	case "add":
		if fs.NArg() == 0 {
			return errors.New("give at least one model name (see: ollama list)")
		}
		if *tenant == "" {
			for _, m := range fs.Args() {
				if err := AddSharedOllama(ctx, r, m); err != nil {
					return err
				}
			}
			return nil
		}
		added, skipped := r.AddTenantOllama(ctx, *tenant, fs.Args())
		for _, a := range added {
			fmt.Println("added", a, "for tenant", *tenant)
		}
		for _, s := range skipped {
			fmt.Println("skipped", s)
		}
	default:
		return errors.New("unknown ollama command: " + sub)
	}
	return nil
}

// AddSharedOllama saves one shared Ollama provider for the given model.
// Exported so embedders can use the same logic.
func AddSharedOllama(ctx context.Context, r *router.Router, model string) error {
	p := types.ProviderConfig{Preset: "ollama", Name: "ollama-" + model, Model: model, Priority: 90, Workers: 1}
	if err := r.SaveProvider(ctx, &p); err != nil {
		return err
	}
	fmt.Printf("added shared provider #%d %q\n", p.ID, p.Name)
	return nil
}
