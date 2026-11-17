package main

import (
        "encoding/json"
        "flag"
        "fmt"
        "os"
        "strings"
        "text/tabwriter"

        "github.com/you/airouter/pkg/preset"
)

// cmdPresets implements `airouter presets` — prints every built-in provider
// preset (connection defaults, no model ids or limits).
func cmdPresets(args []string) error {
        tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
        fmt.Fprintln(tw, "NAME\tKIND\tBASE URL\tKEY ENV\tRESET TZ")
        for _, name := range preset.Names() {
                def := preset.Definitions[name]
                keyEnv := def.APIKeyEnv
                if keyEnv == "" {
                        keyEnv = "(none)"
                }
                fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, def.Kind, def.BaseURL, keyEnv, def.ResetTZ)
        }
        tw.Flush()
        return nil
}

// cmdFreePool implements `airouter free-pool [-category a,b,c] [-json]`.
//
// Prints the curated free-tier model catalog with limits, categories, and
// verification dates. By default shows every entry; pass `-category text` (or
// `tts,stt`, or `all`) to filter.
func cmdFreePool(args []string) error {
        fs := flag.NewFlagSet("free-pool", flag.ExitOnError)
        categoryFlag := fs.String("category", "all", "comma-separated categories to show (text, tts, stt, embedding, image, live, agent, safety, all)")
        asJSON := fs.Bool("json", false, "print as JSON instead of a table")
        _ = fs.Parse(args)

        var categories []string
        if strings.TrimSpace(*categoryFlag) != "all" {
                for _, c := range strings.Split(*categoryFlag, ",") {
                        if c = strings.TrimSpace(c); c != "" {
                                categories = append(categories, c)
                        }
                }
        }
        models := preset.FreePoolByCategory(categories)

        if *asJSON {
                out, _ := json.MarshalIndent(map[string]any{
                        "models":      models,
                        "verified_on": preset.CatalogVerifiedOn().Format("2006-01-02"),
                        "summary":     preset.CatalogSummary(),
                }, "", "  ")
                fmt.Println(string(out))
                return nil
        }

        // Summary line first so users see the shape of the catalog at a glance.
        summary := preset.CatalogSummary()
        fmt.Fprintf(os.Stderr, "catalog: %d models across %d categories (verified %s)\n",
                len(preset.FreePool), len(preset.Categories), preset.CatalogVerifiedOn().Format("2006-01-02"))
        if len(summary) == 0 {
                fmt.Fprintln(os.Stderr, "(empty catalog)")
        } else {
                fmt.Fprintf(os.Stderr, "by category: ")
                for i, c := range preset.Categories {
                        if i > 0 {
                                fmt.Fprintf(os.Stderr, ", ")
                        }
                        fmt.Fprintf(os.Stderr, "%s=%d", c, summary[c])
                }
                fmt.Fprintln(os.Stderr)
        }

        if len(categories) > 0 {
                fmt.Fprintf(os.Stderr, "showing: %s\n", strings.Join(categories, ", "))
        } else {
                fmt.Fprintf(os.Stderr, "showing: all\n")
        }
        fmt.Fprintln(os.Stderr)

        tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
        fmt.Fprintln(tw, "CATEGORY\tPRESET\tMODEL\tRPM\tRPD\tTPM\tASH\tASD\tPRIO\tWORKERS\tVERIFIED\tNOTES")
        for _, m := range models {
                fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
                        m.Category, m.Preset, m.Model,
                        formatLimit(m.RPM), formatLimit(m.RPD), formatLimit(m.TPM),
                        formatLimit(m.ASH), formatLimit(m.ASD),
                        m.Priority, m.Workers, m.VerifiedOn, m.Notes)
        }
        tw.Flush()
        fmt.Fprintf(os.Stderr, "\nlimits move — re-check the provider's console before relying on these numbers.\n")
        fmt.Fprintf(os.Stderr, "sources: aistudio.google.com/docs/rate-limits · console.groq.com/docs/rate-limits\n")
        fmt.Fprintf(os.Stderr, "ASH = audio seconds per hour, ASD = audio seconds per day (TTS/STT only).\n")
        return nil
}

// formatLimit turns an int limit into a human-readable cell value: 0 becomes
// "—" (the dash convention used in the table), other values are printed as-is.
func formatLimit(n int) string {
        if n == 0 {
                return "—"
        }
        return fmt.Sprint(n)
}
