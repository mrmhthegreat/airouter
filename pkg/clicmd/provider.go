package clicmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/mrmhthegreat/airouter/pkg/preset"
	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Provider implements `provider add|list|rm|enable|disable|reset|set-key|limits|import`.
// It calls *airouter.Client (router.Router) methods directly: SaveProvider,
// DeleteProvider, SetDisabled, ClearPause, SetProviderKey, UpdateTenantLimits,
// ImportTenantProviders, Status.
//
// Args:
//
//	["list"]                                     → Status — every provider, live state
//	["add", "-preset", "groq", "-key", "gsk_..."]  → SaveProvider — add one
//	["add", "-preset", "gemini", "-key", "AI...", "-model", "gemini-2.5-flash"]
//	["add", "-name", "my-groq", "-kind", "openai", "-base-url", URL, "-key", "gsk_...", "-model", "openai/gpt-oss-120b"]
//	["rm", "<id>"]                               → DeleteProvider
//	["enable", "<id>"]                           → SetDisabled(false)
//	["disable", "<id>"]                          → SetDisabled(true)
//	["reset", "<id>"]                            → ClearPause (lift cooldown / daily-limit pause)
//	["set-key", "<id>", "-key", "gsk_..."]       → SetProviderKey (stored encrypted)
//	["set-key", "<id>", "-key-stdin"]            → same, key from stdin
//	["limits", "<id>", "-rpm", "30", "-rpd", "1000", ...]  → UpdateTenantLimits (tenant-scoped)
//	["import", "providers.json"]                 → ImportTenantProviders (-tenant) or save each (admin)
//	["-tenant", "ID", "list"]                    → act as one tenant (only its own models)
func Provider(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: provider add|list|rm|enable|disable|reset|set-key|limits|import")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("provider "+sub, flag.ExitOnError)
	db := DbFlag(fs)
	var p types.ProviderConfig
	var noJSON, noSystem, keyStdin bool
	var setKeyFlag, tenantFlag string
	var lim struct{ rpm, rpd, tpm, workers, priority int }
	if sub != "add" {
		fs.StringVar(&tenantFlag, "tenant", "", "act as this tenant: only its own models are listed/changed (default: all, admin view)")
	}
	if sub == "limits" {
		fs.IntVar(&lim.rpm, "rpm", -1, "requests per minute")
		fs.IntVar(&lim.rpd, "rpd", -1, "requests per day")
		fs.IntVar(&lim.tpm, "tpm", -1, "tokens per minute")
		fs.IntVar(&lim.workers, "workers", -1, "max concurrent requests")
		fs.IntVar(&lim.priority, "priority", -1, "lower = tried first")
	}
	if sub == "set-key" {
		fs.StringVar(&setKeyFlag, "key", "", "the new API key (omit to be asked)")
	}
	if sub == "add" {
		fs.StringVar(&p.Preset, "preset", "", "preset: "+strings.Join(preset.Names(), ", "))
		fs.StringVar(&p.Name, "name", "", "unique name (default: preset-model)")
		fs.StringVar(&p.Kind, "kind", "", "openai | ollama")
		fs.StringVar(&p.BaseURL, "base-url", "", "API base url")
		fs.StringVar(&p.APIKeyEnv, "key-env", "", "env var holding the API key")
		fs.StringVar(&p.APIKey, "key", "", "API key to store (encrypted) in the database; omit and use -key-stdin to avoid shell history")
		fs.BoolVar(&keyStdin, "key-stdin", false, "read the API key from stdin")
		fs.StringVar(&p.Model, "model", "", "exact model id")
		fs.StringVar(&p.TenantID, "tenant", "", "restrict to one tenant (default: shared by all)")
		fs.StringVar(&p.ResetTZ, "tz", "", "IANA timezone of the daily reset (e.g. America/Los_Angeles)")
		fs.StringVar(&p.Category, "category", "", "text (default) | tts | stt | embed — request shape this model expects")
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
	_ = parseInterspersed(fs, rest)
	r, err := OpenRouter(*db, router.TenantPolicy{AllowCustomBaseURL: true})
	if err != nil {
		return err
	}
	ctx := context.Background()
	idArg := func() (int64, error) {
		var id int64
		if _, err := fmt.Sscan(fs.Arg(0), &id); err != nil {
			return 0, errors.New("give the provider id (see: provider list)")
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
		if keyStdin {
			k, err := ReadSecret("", "API key")
			if err != nil {
				return err
			}
			p.APIKey = k
		}
		if err := r.SaveProvider(ctx, &p); err != nil {
			return err
		}
		fmt.Printf("added provider #%d %q (%s)\n", p.ID, p.Name, p.Model)
	case "list":
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tOWNER\tCAT\tNAME\tMODEL\tSTATE\tKEY\tUSED/RPD\tRPM\tTPM\tAUDIO SEC (ASH/ASD)\tRESETS IN\tTZ")
		for _, s := range r.Status(ctx, tenantFlag) {
			if tenantFlag != "" && s.TenantID != tenantFlag {
				continue
			}
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
			owner := s.TenantID
			if owner == "" {
				owner = "shared"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%d/%s\t%d\t%d\t%s\t%s\t%s\n",
				s.ID, owner, s.Category, s.Name, s.Model, s.State, s.KeySource, s.UsedToday, rpd, s.RPM, s.TPM,
				audioCol, FmtDur(s.ResetsInSec), s.ResetTZ)
		}
		tw.Flush()
	case "set-key":
		id, err := idArg()
		if err != nil {
			return err
		}
		k, err := ReadSecret(setKeyFlag, "New API key")
		if err != nil {
			return err
		}
		if err := tenantOr(tenantFlag, func() error { return r.SetTenantProviderKey(ctx, tenantFlag, id, k) }, func() error { return r.SetProviderKey(ctx, id, k) }); err != nil {
			return err
		}
		fmt.Printf("key stored (encrypted) for provider #%d; any pause was lifted\n", id)
	case "limits":
		id, err := idArg()
		if err != nil {
			return err
		}
		opt := func(v int) *int {
			if v < 0 {
				return nil
			}
			return &v
		}
		l := router.TenantLimits{RPM: opt(lim.rpm), RPD: opt(lim.rpd), TPM: opt(lim.tpm), Workers: opt(lim.workers), Priority: opt(lim.priority)}
		owner := tenantFlag
		if owner == "" {
			ps, _ := r.Store().ListProviders(ctx)
			for _, p := range ps {
				if p.ID == id {
					owner = p.TenantID
				}
			}
		}
		if owner == "" {
			return errors.New("that model is shared; edit it with `provider import` or the admin API")
		}
		if err := r.UpdateTenantLimits(ctx, owner, id, l); err != nil {
			return err
		}
		fmt.Printf("limits updated for provider #%d\n", id)
	case "rm":
		id, err := idArg()
		if err != nil {
			return err
		}
		return tenantOr(tenantFlag, func() error { return r.DeleteTenantProvider(ctx, tenantFlag, id) }, func() error { return r.DeleteProvider(ctx, id) })
	case "reset":
		id, err := idArg()
		if err != nil {
			return err
		}
		return tenantOr(tenantFlag, func() error { return r.ResetTenantProvider(ctx, tenantFlag, id) }, func() error { return r.ClearPause(ctx, id) })
	case "enable", "disable":
		id, err := idArg()
		if err != nil {
			return err
		}
		return tenantOr(tenantFlag, func() error { return r.SetTenantProviderDisabled(ctx, tenantFlag, id, sub == "disable") }, func() error { return r.SetDisabled(ctx, id, sub == "disable") })
	case "import":
		if fs.NArg() == 0 {
			return errors.New("usage: provider import [-tenant ID] providers.json")
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
		if tenantFlag != "" {
			added, failed := r.ImportTenantProviders(ctx, tenantFlag, f.Providers)
			for _, n := range added {
				fmt.Println("imported", n, "for tenant", tenantFlag)
			}
			for n, why := range failed {
				fmt.Fprintf(os.Stderr, "skipped %s: %s\n", n, why)
			}
			return nil
		}
		existing, _ := r.Store().ListProviders(ctx)
		for i, q := range f.Providers {
			if q.Priority == 0 {
				q.Priority = (i + 1) * 10
			}
			for _, e := range existing {
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

// tenantOr runs the tenant-scoped action when -tenant was given (ownership is
// then enforced), otherwise the admin action.
func tenantOr(tenant string, scoped, admin func() error) error {
	if tenant != "" {
		return scoped()
	}
	return admin()
}
