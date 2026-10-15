package configedit

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
)

func TestPruneEmptyOwnedObjectsKeepsCommentsAndEscapedModelKeys(t *testing.T) {
	raw := []byte("\xef\xbb\xbf{\r\n  \"providers\": {\r\n    \"surplustoken\": {\r\n      \"models\": {\"model.a\": {}, \"user\": {}}\r\n    }\r\n  },\r\n  \"commented\": { /* keep this */ }\r\n}\r\n")
	cleaned, err := PruneEmptyObjects(raw, []string{"providers", "providers.surplustoken", "providers.surplustoken.models", `providers.surplustoken.models.model\.a`, "commented"})
	if err != nil {
		t.Fatal(err)
	}
	parsed := jsonc.ToJSON(cleaned[3:])
	if gjson.GetBytes(parsed, `providers.surplustoken.models.model\.a`).Exists() {
		t.Fatal("owned model with a dotted identifier was not removed")
	}
	if !gjson.GetBytes(parsed, "providers.surplustoken.models.user").Exists() || !strings.Contains(string(cleaned), "/* keep this */") {
		t.Fatal("unowned object or added comment was removed")
	}
	if strings.Contains(strings.ReplaceAll(string(cleaned), "\r\n", ""), "\n") {
		t.Fatal("CRLF was not preserved")
	}
}
