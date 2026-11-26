package clicmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// Tenant implements `tenant add NAME [-limit N] [-tz ZONE] | tenant list |
// tenant rotate NAME`. Calls CreateTenant, RotateTenantKey, ListTenants.
//
// Args:
//
//	["add", "acme", "-limit", "1000", "-tz", "UTC"]   → CreateTenant (prints API key once)
//	["add", "acme", "-limit", "50", "-pool-only"]     → cap counts only shared-pool answers
//	["cap", "acme", "-limit", "200", "-pool-only=false", "-disable"] → change the cap later
//	["list"]                                          → ListTenants
//	["rotate", "acme"]                                → RotateTenantKey (prints new key once)
func Tenant(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tenant add NAME [-limit N] [-pool-only] [-tz ZONE] | tenant cap NAME [-limit N] [-pool-only[=false]] [-disable|-enable] | tenant list | tenant rotate NAME")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("tenant "+sub, flag.ExitOnError)
	db := DbFlag(fs)
	limit := fs.Int("limit", 0, "tenant daily request limit (0 = unlimited)")
	tz := fs.String("tz", "UTC", "tenant timezone")
	poolOnly := fs.Bool("pool-only", false, "the cap counts only answers from the shared pool; the tenant's own keys are free and keep working after the cap")
	disable := fs.Bool("disable", false, "(cap) turn the tenant off")
	enable := fs.Bool("enable", false, "(cap) turn the tenant back on")
	_ = parseInterspersed(fs, rest)
	r, err := OpenRouter(*db)
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch sub {
	case "add":
		if fs.NArg() == 0 {
			return errors.New("give a tenant id, e.g. tenant add acme")
		}
		t, key, err := r.CreateTenant(ctx, types.Tenant{ID: fs.Arg(0), TZ: *tz, DailyRequestLimit: *limit, PoolOnlyCap: *poolOnly})
		if err != nil {
			return err
		}
		fmt.Printf("tenant %q created.\nAPI key (shown once): %s\n", t.ID, key)
	case "cap":
		if fs.NArg() == 0 {
			return errors.New("give a tenant id, e.g. tenant cap acme -limit 500")
		}
		cur, err := r.Store().TenantByID(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		if cur == nil {
			return types.ErrUnknownTenant
		}
		lim, pool, dis := cur.DailyRequestLimit, cur.PoolOnlyCap, cur.Disabled
		fs.Visit(func(f *flag.Flag) { // only change what was given
			switch f.Name {
			case "limit":
				lim = *limit
			case "pool-only":
				pool = *poolOnly
			}
		})
		if *disable {
			dis = true
		}
		if *enable {
			dis = false
		}
		t, err := r.UpdateTenantCap(ctx, fs.Arg(0), lim, pool, dis)
		if err != nil {
			return err
		}
		fmt.Printf("tenant %q: daily limit %d, pool-only %v, disabled %v\n", t.ID, t.DailyRequestLimit, t.PoolOnlyCap, t.Disabled)
	case "rotate":
		if fs.NArg() == 0 {
			return errors.New("give a tenant id, e.g. tenant rotate acme")
		}
		key, err := r.RotateTenantKey(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Printf("new API key for %q (shown once; the old key no longer works): %s\n", fs.Arg(0), key)
	case "list":
		ts, _ := r.Store().ListTenants(ctx)
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tTZ\tDAILY LIMIT\tCOUNTS\tSTATE\tCREATED")
		for _, t := range ts {
			counts, state := "all requests", "active"
			if t.PoolOnlyCap {
				counts = "pool only"
			}
			if t.Disabled {
				state = "disabled"
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n", t.ID, t.TZ, t.DailyRequestLimit, counts, state, t.CreatedAt.Format("2006-01-02"))
		}
		tw.Flush()
	default:
		return errors.New("unknown tenant command: " + sub)
	}
	return nil
}

// Usage implements `usage [-tenant ID] [-day YYYY-MM-DD]`. Calls the
// *airouter.Client.Usage method — per-tenant usage report across every
// provider, with per-day breakdowns.
//
// Args:
//
//	[]                                  → today, default tenant
//	["-tenant", "acme"]                 → one tenant's usage
//	["-day", "2026-01-15"]              → historical day (per provider's own TZ)
func Usage(args []string) error {
	fs := flag.NewFlagSet("usage", flag.ExitOnError)
	db := DbFlag(fs)
	tenant := fs.String("tenant", types.DefaultTenantID, "tenant id")
	day := fs.String("day", "", "YYYY-MM-DD (default: today per provider)")
	_ = fs.Parse(args)
	r, err := OpenRouter(*db)
	if err != nil {
		return err
	}
	rep, err := r.Usage(context.Background(), *tenant, *day)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "tenant %s: %d requests, %d tokens (tenant daily limit: %d)\n\n",
		rep.Tenant, rep.RequestsToday, rep.TokensToday, rep.DailyLimit)
	fmt.Fprintln(tw, "PROVIDER\tMODEL\tDAY\tYOURS\tTOKENS\tERRORS\tALL TENANTS\tLIMIT/DAY\tRESETS IN")
	for _, u := range rep.Providers {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n",
			u.Provider, u.Model, u.Day, u.TenantRequests, u.TenantTokens, u.TenantErrors,
			u.AllRequests, u.LimitPerDay, FmtDur(u.ResetsInSec))
	}
	tw.Flush()
	return nil
}
