package clicmd

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

// Bootstrap implements `bootstrap [-preset NAME] [-category a,b,c] [-overwrite]
// [-store-keys] [-tenant ID]`. It calls BootstrapProvidersOpts on the
// *airouter.Client — imports entries from the curated free-pool catalog into
// the DB, skipping entries whose env var is unset (so a fresh machine with no
// API keys does not end up with a dozen broken providers).
//
// Args (all optional flags):
//
//	["-overwrite"]                          update existing same-name providers in place
//	["-preset", "groq"]                     only import entries for this preset
//	["-category", "text"]                   default: text only. Pass "all" for everything
//	["-category", "text,tts,stt"]           only these categories
//	["-store-keys"]                         copy keys found in env vars into the DB (encrypted)
//	["-tenant", "ID"]                       import the models as PRIVATE to this tenant
//
// Returns the names added and the names skipped (with reasons).
func Bootstrap(args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	db := DbFlag(fs)
	onlyPreset := fs.String("preset", "", "only import entries for this preset (e.g. groq)")
	categoryFlag := fs.String("category", "text", "comma-separated categories to import (default: text). Pass 'all' for everything, or e.g. 'text,tts,stt'")
	overwrite := fs.Bool("overwrite", false, "update existing same-name providers in place")
	storeKeys := fs.Bool("store-keys", false, "copy keys found in env vars into the database (encrypted) so the env vars are no longer needed")
	tenant := fs.String("tenant", "", "import the models as PRIVATE to this tenant (keys from the env vars are copied into the DB for them)")
	_ = fs.Parse(args)

	var categories []string
	if strings.TrimSpace(*categoryFlag) == "all" {
		categories = []string{}
	} else {
		for _, c := range strings.Split(*categoryFlag, ",") {
			if c = strings.TrimSpace(c); c != "" {
				categories = append(categories, c)
			}
		}
	}

	r, err := OpenRouter(*db)
	if err != nil {
		return err
	}
	added, skipped, err := r.BootstrapProvidersOpts(context.Background(), router.BootstrapOptions{Overwrite: *overwrite, Preset: *onlyPreset, Categories: categories, StoreKeys: *storeKeys, TenantID: *tenant})
	if err != nil {
		return err
	}
	if len(added) == 0 && len(skipped) == 0 {
		return errors.New("no providers matched — check the -preset and -category values")
	}
	fmt.Fprintf(os.Stderr, "importing categories: %s\n", FormatCategoriesLabel(categories))
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
	fmt.Println("\nnext: set the env vars named above, then `provider list` to see live state.")
	return nil
}
