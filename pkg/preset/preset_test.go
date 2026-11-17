package preset

import (
	"testing"

	"github.com/you/airouter/pkg/types"
)

func TestApplyFillsDefaults(t *testing.T) {
	p := &types.ProviderConfig{Preset: "groq", Model: "llama-3.3-70b-versatile"}
	if err := Apply(p); err != nil {
		t.Fatal(err)
	}
	if p.Kind != "openai" {
		t.Fatalf("Kind: %q", p.Kind)
	}
	if p.BaseURL != "https://api.groq.com/openai/v1" {
		t.Fatalf("BaseURL: %q", p.BaseURL)
	}
	if p.APIKeyEnv != "GROQ_API_KEY" {
		t.Fatalf("APIKeyEnv: %q", p.APIKeyEnv)
	}
	if p.ResetTZ != "UTC" {
		t.Fatalf("ResetTZ: %q", p.ResetTZ)
	}
	if p.Name != "groq-llama-3.3-70b-versatile" {
		t.Fatalf("Name: %q", p.Name)
	}
}

func TestApplyGemmaSetsNoJSONNoSystem(t *testing.T) {
	p := &types.ProviderConfig{Preset: "gemma", Model: "gemma-3-27b-it"}
	if err := Apply(p); err != nil {
		t.Fatal(err)
	}
	if p.JSONMode == nil || *p.JSONMode {
		t.Fatalf("gemma JSONMode should be false")
	}
	if p.SystemRole == nil || *p.SystemRole {
		t.Fatalf("gemma SystemRole should be false")
	}
}

func TestApplyUnknownPresetErrors(t *testing.T) {
	p := &types.ProviderConfig{Preset: "nope", Model: "x"}
	if err := Apply(p); err == nil {
		t.Fatal("expected error")
	}
}

func TestApplyKeepsExplicitValues(t *testing.T) {
	custom := "https://my-proxy.example.com/v1"
	p := &types.ProviderConfig{Preset: "groq", Model: "m", BaseURL: custom}
	if err := Apply(p); err != nil {
		t.Fatal(err)
	}
	if p.BaseURL != custom {
		t.Fatalf("Apply overrode explicit BaseURL: %q", p.BaseURL)
	}
}

func TestFreeModelToProviderConfig(t *testing.T) {
	if len(FreePool) == 0 {
		t.Fatal("FreePool empty")
	}
	m := FreePool[0]
	cfg := m.ToProviderConfig()
	if cfg.Model != m.Model {
		t.Fatalf("Model: %q", cfg.Model)
	}
	if cfg.RPM != m.RPM || cfg.RPD != m.RPD || cfg.TPM != m.TPM {
		t.Fatalf("limits not copied: %+v", cfg)
	}
	if cfg.Priority != m.Priority {
		t.Fatalf("Priority: %d", cfg.Priority)
	}
	// preset defaults applied
	if cfg.BaseURL == "" {
		t.Fatal("BaseURL not applied from preset")
	}
}

func TestFreePoolForFilter(t *testing.T) {
	groq := FreePoolFor("groq")
	if len(groq) == 0 {
		t.Fatal("no groq entries")
	}
	for _, m := range groq {
		if m.Preset != "groq" {
			t.Fatalf("non-groq entry: %+v", m)
		}
	}
	all := FreePoolFor("")
	if len(all) != len(FreePool) {
		t.Fatalf("FreePoolFor(\"\") returned %d, want %d", len(all), len(FreePool))
	}
}

func TestNamesSorted(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("no presets")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("Names not sorted: %v", names)
		}
	}
}

func TestDefinitionsContainsAllPresetsReferencedByFreePool(t *testing.T) {
	for _, m := range FreePool {
		if _, ok := Definitions[m.Preset]; !ok {
			t.Fatalf("FreePool entry %q references unknown preset %q", m.Model, m.Preset)
		}
	}
}
