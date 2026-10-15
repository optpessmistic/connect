package httpapi

import (
	"github.com/tidwall/gjson"
	"os"
	"path/filepath"
	"strings"
	"surplustoken-connect/internal/configedit"
	"surplustoken-connect/internal/configuration"
	"surplustoken-connect/internal/gateway"
	"testing"
)

func TestOpenCodeV2TemplatePreservesModelChoices(t *testing.T) {
	patches := openCodeV2Patches(gateway.Key{Value: "test-key-secret"}, gateway.Group{Platform: "openai"}, []string{"gpt-5.5", "other/model.v2"})
	raw, err := configedit.PatchJSON([]byte("{\n // user comment\n \"model\":\"original/default\", \"agents\":{\"title\":{\"model\":\"original/small\"},\"custom\":{\"model\":\"keep/model\"}}\n}"), patches, nil)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{"providers.surplustoken.package": "@opencode/ai/providers/openai-compatible/responses", "providers.surplustoken.settings.apiKey": "test-key-secret", "model": "original/default", "agents.title.model": "original/small", "agents.custom.model": "keep/model", "providers.surplustoken.models.other/model\\.v2.modelID": "other/model.v2"}
	for field, want := range checks {
		if value := gjson.GetBytes(raw, field).String(); value != want {
			t.Fatalf("%s mismatch", field)
		}
	}
	if !strings.Contains(string(raw), "// user comment") || gjson.GetBytes(raw, "provider").Exists() || gjson.GetBytes(raw, "small_model").Exists() {
		t.Fatal("legacy format written or comments lost")
	}
	if got := openCodeV2Patches(gateway.Key{}, gateway.Group{Platform: "composite"}, nil); got != nil {
		t.Fatal("ambiguous composite protocol guessed")
	}
}

func TestOpenCodeProtocolSwitchReconcilesOnlyOwnedTemplateFields(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "opencode.json")
	engine, err := configuration.New(filepath.Join(root, "backups"), map[string]string{"opencode": path})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"providers":{"other":{"name":"keep"},"surplustoken":{"models":{"user-model":{"name":"custom"}}}},"model":"other/default"}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	apply := func(platform, model string) configuration.Record {
		t.Helper()
		patches := openCodeV2Patches(gateway.Key{Value: "fixture-" + platform}, gateway.Group{Platform: platform}, []string{model})
		plan, err := engine.PlanManaged("fixture", "opencode", patches, "providers.surplustoken")
		if err != nil {
			t.Fatal(err)
		}
		record, err := engine.Apply("fixture", plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	apply("openai", "model.a")
	record := apply("anthropic", "model.b")
	raw, _ := os.ReadFile(path)
	if gjson.GetBytes(raw, `providers.surplustoken.models.model\.a`).Exists() || gjson.GetBytes(raw, "providers.surplustoken.settings.transport").Exists() {
		t.Fatal("previous protocol fields or models survived a switch")
	}
	for field, want := range map[string]string{
		"providers.other.name":                          "keep",
		"providers.surplustoken.models.user-model.name": "custom",
		"providers.surplustoken.package":                "@opencode/ai/providers/anthropic-compatible",
		"model":                                         "other/default",
	} {
		if gjson.GetBytes(raw, field).String() != want {
			t.Fatalf("unexpected value for %s", field)
		}
	}
	restore, err := engine.Restore("fixture", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Apply("fixture", restore.ID); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if !gjson.GetBytes(raw, `providers.surplustoken.models.model\.a`).Exists() || gjson.GetBytes(raw, `providers.surplustoken.models.model\.b`).Exists() {
		t.Fatal("protocol switch could not be reversed")
	}
}

func TestOpenCodeTemplateDoesNotAddDefaultOrTitleModel(t *testing.T) {
	patches := openCodeV2Patches(gateway.Key{Value: "fixture"}, gateway.Group{Platform: "openai"}, []string{"gpt-5.5"})
	raw, err := configedit.PatchJSON([]byte("{}"), patches, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(raw, "model").Exists() || gjson.GetBytes(raw, "agents").Exists() {
		t.Fatal("template assigned model choices")
	}
	if gjson.GetBytes(raw, "providers.surplustoken.settings.transport").String() != "http" {
		t.Fatal("gateway template must use HTTP")
	}
}
