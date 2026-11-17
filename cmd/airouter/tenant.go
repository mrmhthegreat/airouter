package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// cmdTenant implements `airouter tenant add NAME [-limit N] [-tz ZONE] | airouter tenant list`.
func cmdTenant(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: airouter tenant add NAME [-limit N] [-tz ZONE] | airouter tenant list")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("tenant "+sub, flag.ExitOnError)
	db := dbFlag(fs)
	limit := fs.Int("limit", 0, "tenant daily request limit (0 = unlimited)")
	tz := fs.String("tz", "UTC", "tenant timezone")
	_ = fs.Parse(rest)
	r, err := openRouter(*db)
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch sub {
	case "add":
		if fs.NArg() == 0 {
			return errors.New("give a tenant id, e.g. airouter tenant add acme")
		}
		t, key, err := r.CreateTenant(ctx, types.Tenant{ID: fs.Arg(0), TZ: *tz, DailyRequestLimit: *limit})
		if err != nil {
			return err
		}
		fmt.Printf("tenant %q created.\nAPI key (shown once): %s\n", t.ID, key)
	case "list":
		ts, _ := r.Store().ListTenants(ctx)
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tTZ\tDAILY LIMIT\tCREATED")
		for _, t := range ts {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", t.ID, t.TZ, t.DailyRequestLimit, t.CreatedAt.Format("2006-01-02"))
		}
		tw.Flush()
	default:
		return errors.New("unknown tenant command: " + sub)
	}
	return nil
}

// cmdUsage implements `airouter usage [-tenant ID] [-day YYYY-MM-DD]`.
func cmdUsage(args []string) error {
	fs := flag.NewFlagSet("usage", flag.ExitOnError)
	db := dbFlag(fs)
	tenant := fs.String("tenant", types.DefaultTenantID, "tenant id")
	day := fs.String("day", "", "YYYY-MM-DD (default: today per provider)")
	_ = fs.Parse(args)
	r, err := openRouter(*db)
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
			u.AllRequests, u.LimitPerDay, fmtDur(u.ResetsInSec))
	}
	tw.Flush()
	return nil
}

// fmtDur turns a number of seconds into "1h05m" / "0h02m" style strings.
func fmtDur(sec int64) string { return fmt.Sprintf("%dh%02dm", sec/3600, sec%3600/60) }
