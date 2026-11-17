package main

import (
        "context"
        "encoding/json"
        "errors"
        "flag"
        "fmt"
        "os"
        "strings"
        "text/tabwriter"

        "github.com/you/airouter/pkg/preset"
        "github.com/you/airouter/pkg/types"
)

// cmdProvider implements `airouter provider add|list|rm|enable|disable|reset|import`.
func cmdProvider(args []string) error {
        if len(args) == 0 {
                return errors.New("usage: airouter provider add|list|rm|enable|disable|reset|import")
        }
        sub, rest := args[0], args[1:]
        fs := flag.NewFlagSet("provider "+sub, flag.ExitOnError)
        db := dbFlag(fs)
        var p types.ProviderConfig
        var noJSON, noSystem bool
        if sub == "add" {
                fs.StringVar(&p.Preset, "preset", "", "preset: "+strings.Join(preset.Names(), ", "))
                fs.StringVar(&p.Name, "name", "", "unique name (default: preset-model)")
                fs.StringVar(&p.Kind, "kind", "", "openai | ollama")
                fs.StringVar(&p.BaseURL, "base-url", "", "API base url")
                fs.StringVar(&p.APIKeyEnv, "key-env", "", "env var holding the API key")
                fs.StringVar(&p.APIKey, "key", "", "API key stored in the database (prefer -key-env)")
                fs.StringVar(&p.Model, "model", "", "exact model id")
                fs.StringVar(&p.TenantID, "tenant", "", "restrict to one tenant (default: shared by all)")
                fs.StringVar(&p.ResetTZ, "tz", "", "IANA timezone of the daily reset (e.g. America/Los_Angeles)")
                fs.StringVar(&p.Category, "category", "", "text (default) | tts | stt — request shape this model expects")
                fs.IntVar(&p.Priority, "priority", 0, "lower = tried first")
                fs.IntVar(&p.RPM, "rpm", 0, "requests per minute (0 = no local cap)")
                fs.IntVar(&p.RPD, "rpd", 0, "requests per day (0 = no local cap)")
                fs.IntVar(&p.TPM, "tpm", 0, "tokens per minute (0 = no local cap)")
                fs.IntVar(&p.ASH, "ash", 0, "audio seconds per hour (TTS/STT only, 0 = no local cap)")
                fs.IntVar(&p.ASD, "asd", 0, "audio seconds per day (TTS/STT only, 0 = no local cap)")
                fs.IntVar(&p.Workers, "workers", 0, "max concurrent requests")
                fs.IntVar(&p.UsedTodayHint, "used-today", 0, "requests you already used today")
                fs.BoolVar(&noJSON, "no-json-mode", false, "model rejects response_format (e.g. Gemma)")
                fs.BoolVar(&noSystem, "no-system-role", false, "model rejects system messages (e.g. Gemma)")
        }
        _ = fs.Parse(rest)
        r, err := openRouter(*db)
        if err != nil {
                return err
        }
        ctx := context.Background()
        idArg := func() (int64, error) {
                var id int64
                if _, err := fmt.Sscan(fs.Arg(0), &id); err != nil {
                        return 0, errors.New("give the provider id (see: airouter provider list)")
                }
                return id, nil
        }
        switch sub {
        case "add":
                f := false
                if noJSON {
                        p.JSONMode = &f
                }
                if noSystem {
                        p.SystemRole = &f
                }
                if err := r.SaveProvider(ctx, &p); err != nil {
                        return err
                }
                fmt.Printf("added provider #%d %q (%s)\n", p.ID, p.Name, p.Model)
        case "list":
                tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
                fmt.Fprintln(tw, "ID\tCAT\tNAME\tMODEL\tSTATE\tUSED/RPD\tRPM\tTPM\tAUDIO SEC (ASH/ASD)\tRESETS IN\tTZ")
                for _, s := range r.Status(ctx, "") {
                        rpd := "-"
                        if s.RPD > 0 {
                                rpd = fmt.Sprint(s.RPD)
                        }
                        audioCol := "-"
                        if s.Category == "tts" || s.Category == "stt" {
                                ashStr := "-"
                                if s.ASH > 0 {
                                        ashStr = fmt.Sprint(s.ASH)
                                }
                                asdStr := "-"
                                if s.ASD > 0 {
                                        asdStr = fmt.Sprint(s.ASD)
                                }
                                audioCol = fmt.Sprintf("%d/%s/%s", s.AudioSecToday, ashStr, asdStr)
                        }
                        fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d/%s\t%d\t%d\t%s\t%s\t%s\n",
                                s.ID, s.Category, s.Name, s.Model, s.State, s.UsedToday, rpd, s.RPM, s.TPM,
                                audioCol, fmtDur(s.ResetsInSec), s.ResetTZ)
                }
                tw.Flush()
        case "rm":
                id, err := idArg()
                if err != nil {
                        return err
                }
                return r.DeleteProvider(ctx, id)
        case "reset":
                id, err := idArg()
                if err != nil {
                        return err
                }
                return r.ClearPause(ctx, id)
        case "enable", "disable":
                id, err := idArg()
                if err != nil {
                        return err
                }
                return r.SetDisabled(ctx, id, sub == "disable")
        case "import":
                if fs.NArg() == 0 {
                        return errors.New("usage: airouter provider import providers.json")
                }
                raw, err := os.ReadFile(fs.Arg(0))
                if err != nil {
                        return err
                }
                var f struct {
                        Providers []types.ProviderConfig `json:"providers"`
                }
                if err := json.Unmarshal(raw, &f); err != nil {
                        return err
                }
                existing, _ := r.Store().ListProviders(ctx)
                for i, q := range f.Providers {
                        if q.Priority == 0 {
                                q.Priority = (i + 1) * 10
                        }
                        for _, e := range existing { // same tenant+name → update in place
                                if e.Name == q.Name && e.TenantID == q.TenantID {
                                        q.ID = e.ID
                                }
                        }
                        if err := r.SaveProvider(ctx, &q); err != nil {
                                return fmt.Errorf("%s: %w", q.Name, err)
                        }
                        fmt.Printf("imported #%d %s (%s)\n", q.ID, q.Name, q.Model)
                }
        default:
                return errors.New("unknown provider command: " + sub)
        }
        return nil
}
