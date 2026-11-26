package clicmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/mrmhthegreat/airouter/pkg/router"
	"github.com/mrmhthegreat/airouter/pkg/types"
)

// kvFlag collects repeated -label key=value flags.
type kvFlag map[string]string

func (k kvFlag) String() string { return fmt.Sprint(map[string]string(k)) }
func (k kvFlag) Set(v string) error {
	key, val, ok := strings.Cut(v, "=")
	if !ok || strings.TrimSpace(key) == "" {
		return errors.New("use key=value, e.g. -label gender=female")
	}
	k[strings.TrimSpace(key)] = strings.TrimSpace(val)
	return nil
}

// Voice implements `voice list|clone|rm` for ElevenLabs voices.
//
// Args:
//
//	["list", "-search", "female"]                       → voices this tenant can use
//	["clone", "-name", "Ali", "-consent", "a.mp3", "b.wav"] → Instant Voice Clone from recordings
//	["share", "VOICE_ID"] / ["unshare", "VOICE_ID"]     → operator: let every tenant use a voice you cloned (or stop)
//	["rm", "VOICE_ID"]                                  → delete a voice you cloned
func Voice(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: voice list [-search S] [-provider P] [-json] | voice clone -name N -consent [-desc D] [-label k=v] [-remove-noise] FILE... | voice share|unshare VOICE_ID | voice rm VOICE_ID")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("voice "+sub, flag.ExitOnError)
	db := DbFlag(fs)
	tenant := fs.String("tenant", types.DefaultTenantID, "tenant id")
	provider := fs.String("provider", "", "pin one ElevenLabs provider by name")
	search := fs.String("search", "", "(list) filter by name, description, category or label")
	asJSON := fs.Bool("json", false, "(list) JSON output")
	name := fs.String("name", "", "(clone) name of the new voice")
	desc := fs.String("desc", "", "(clone) description")
	consent := fs.Bool("consent", false, "(clone) I confirm I have the speaker's permission to clone this voice")
	noise := fs.Bool("remove-noise", false, "(clone) remove background noise from the samples")
	labels := kvFlag{}
	fs.Var(labels, "label", "(clone) label like gender=female (repeatable)")
	_ = parseInterspersed(fs, rest)

	r, err := OpenRouter(*db)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	switch sub {
	case "list":
		vs, err := r.ListVoices(ctx, *tenant, router.ListVoicesOptions{Provider: *provider, Search: *search})
		if err != nil {
			return err
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{"voices": vs})
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tCATEGORY\tMINE\tLABELS\tPROVIDER")
		for _, v := range vs {
			var ls []string
			for k, val := range v.Labels {
				ls = append(ls, k+"="+val)
			}
			mine := ""
			if v.Cloned {
				mine = "cloned by you"
			}
			if v.Shared {
				if mine != "" {
					mine += ", "
				}
				mine += "shared with all"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", v.ID, v.Name, v.Category, mine, strings.Join(ls, ","), v.Provider)
		}
		return tw.Flush()
	case "clone":
		if fs.NArg() == 0 {
			return errors.New("give at least one recording, e.g. voice clone -name Ali -consent sample1.mp3 sample2.wav")
		}
		req := types.CloneVoiceRequest{TenantID: *tenant, Name: *name, Description: *desc, Provider: *provider,
			ConsentConfirmed: *consent, RemoveBackgroundNoise: *noise, Labels: labels}
		for _, f := range fs.Args() {
			data, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			req.Samples = append(req.Samples, types.VoiceSample{Filename: filepath.Base(f), Data: data})
		}
		v, err := r.CloneVoice(ctx, req)
		if err != nil {
			return err
		}
		fmt.Printf("voice %q cloned. id=%s provider=%s\nUse it:  airouter speak -voice %q -o out.mp3 -format mp3 \"Hello\"\n", v.Name, v.ID, v.Provider, v.Name)
		if v.RequiresVerification {
			fmt.Println("note: ElevenLabs asks for a verification step before this voice can be used.")
		}
		return nil
	case "share", "unshare":
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: voice %s VOICE_ID (operator: the voice must have been cloned by the default tenant)", sub)
		}
		if err := r.ShareVoice(ctx, *tenant, fs.Arg(0), sub == "share"); err != nil {
			return err
		}
		if sub == "share" {
			fmt.Println("shared: every tenant that can use this ElevenLabs provider can now list and speak with", fs.Arg(0), "(its usage is billed to the provider's account)")
		} else {
			fmt.Println("unshared: only you can use", fs.Arg(0))
		}
		return nil
	case "rm", "delete":
		if fs.NArg() != 1 {
			return errors.New("usage: voice rm VOICE_ID")
		}
		if err := r.DeleteVoice(ctx, *tenant, fs.Arg(0)); err != nil {
			return err
		}
		fmt.Println("deleted", fs.Arg(0))
		return nil
	}
	return fmt.Errorf("unknown voice command %q (list, clone, share, unshare, rm)", sub)
}
