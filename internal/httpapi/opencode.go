package httpapi

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"surplustoken-connect/internal/configedit"
	"surplustoken-connect/internal/gateway"
)

//go:embed templates/opencode-openai.json
var openAIProviderTemplate []byte

var modelPattern = regexp.MustCompile(`^[a-zA-Z0-9_.:/-]+$`)

// Native v2 provider format. Models belong to the selected Key, not a global
// frontend selection. Existing unrelated provider/agent fields remain intact.
// Apply this template through Engine.PlanManaged so obsolete fields from a prior
// Key/protocol are released without deleting user-owned model properties.
func openCodeV2Patches(key gateway.Key, group gateway.Group, models []string) []configedit.KV {
	pkg := "@opencode/ai/providers/openai-compatible"
	canonical := ""
	switch group.Platform {
	case "openai":
		pkg = "@opencode/ai/providers/openai-compatible/responses"
		canonical = "openai"
	case "anthropic":
		pkg = "@opencode/ai/providers/anthropic-compatible"
		canonical = "anthropic"
	case "deepseek":
		canonical = "deepseek"
	case "kimi":
		canonical = "moonshotai"
	default:
		return nil
	}
	patches := []configedit.KV{
		{Path: "providers.surplustoken.package", Value: pkg},
		{Path: "providers.surplustoken.name", Value: "SurplusToken"},
		{Path: "providers.surplustoken.settings.baseURL", Value: gateway.Origin + "/v1"},
		{Path: "providers.surplustoken.settings.apiKey", Value: key.Value},
	}
	if group.Platform == "openai" {
		var template struct {
			Name, Canonical, Package string
			Settings                 struct {
				BaseURL   string `json:"baseURL"`
				Transport string
			}
		}
		if json.Unmarshal(openAIProviderTemplate, &template) != nil {
			return nil
		}
		patches = []configedit.KV{
			{Path: "providers.surplustoken.name", Value: template.Name},
			{Path: "providers.surplustoken.canonical", Value: template.Canonical},
			{Path: "providers.surplustoken.package", Value: template.Package},
			{Path: "providers.surplustoken.settings.baseURL", Value: template.Settings.BaseURL},
			{Path: "providers.surplustoken.settings.transport", Value: template.Settings.Transport},
			{Path: "providers.surplustoken.settings.apiKey", Value: key.Value},
		}
		canonical = ""
	}
	if canonical != "" {
		patches = append(patches, configedit.KV{Path: "providers.surplustoken.canonical", Value: canonical})
	}
	for _, model := range models {
		if len(model) > 200 || !modelPattern.MatchString(model) {
			continue
		}
		field := "providers.surplustoken.models." + escape(model)
		patches = append(patches, configedit.KV{Path: field + ".name", Value: model}, configedit.KV{Path: field + ".modelID", Value: model})
	}
	return patches
}
