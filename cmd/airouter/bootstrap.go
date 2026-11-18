package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mrmhthegreat/airouter/pkg/preset"
	"github.com/mrmhthegreat/airouter/pkg/router"
)

// cmdBootstrap implements `airouter bootstrap [-preset NAME] [-category a,b,c] [-overwrite]`.
//
// It imports every entry from the curated free-pool catalog into the database,
// skipping entries whose env var is unset (so a fresh machine with no API keys
// does not end up with a dozen broken providers) and entries whose category is
// not in `-category` (defaults to "text" — chat-capable models only, since
// airouter's request shape only works for those).
func cmdBootstrap(args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	db := dbFlag(fs)
	onlyPreset := fs.String("preset", "", "only import entries for this preset (e.g. groq)")
	categoryFlag := fs.String("category", "text", "comma-separated categories to import (default: text). Pass 'all' for everything, or e.g. 'text,tts,stt'")
	overwrite := fs.Bool("overwrite", false, "update existing same-name providers in place")
	storeKeys := fs.Bool("store-keys", false, "copy keys found in env vars into the database (encrypted) so the env vars are no longer needed")
	_ = fs.Parse(args)

	var categories []string
	if strings.TrimSpace(*categoryFlag) == "all" {
		// Empty (non-nil) slice tells BootstrapProviders "every category".
		categories = []string{}
	} else {
		for _, c := range strings.Split(*categoryFlag, ",") {
			if c = strings.TrimSpace(c); c != "" {
				categories = append(categories, c)
			}
		}
	}

	r, err := openRouter(*db)
	if err != nil {
		return err
	}
	added, skipped, err := r.BootstrapProvidersOpts(context.Background(), router.BootstrapOptions{Overwrite: *overwrite, Preset: *onlyPreset, Categories: categories, StoreKeys: *storeKeys})
	if err != nil {
		return err
	}
	if len(added) == 0 && len(skipped) == 0 {
		return errors.New("no providers matched — check the -preset and -category values")
	}
	fmt.Fprintf(os.Stderr, "importing categories: %s\n", formatCategoriesLabel(categories))
	fmt.Printf("added %d provider(s):\n", len(added))
	for _, name := range added {
		fmt.Printf("  + %s\n", name)
	}
	if len(skipped) > 0 {
		fmt.Printf("\nskipped %d:\n", len(skipped))
		for _, why := range skipped {
			fmt.Printf("  - %s\n", why)
		}
	}
	fmt.Printf("\ncatalog verified: %s\n", preset.CatalogVerifiedOn().Format("2006-01-02"))
	fmt.Println("limits move — re-check the provider's console before relying on these numbers.")
	fmt.Println("\nnext: set the env vars named above, then `airouter provider list` to see live state.")
	return nil
}

// formatCategoriesLabel pretty-prints the categories list for the stderr banner.
// Mirrors the BootstrapProviders convention: nil = default text, empty = all.
func formatCategoriesLabel(cats []string) string {
	if cats == nil {
		return "text (default)"
	}
	if len(cats) == 0 {
		return "all"
	}
	return strings.Join(cats, ", ")
}
