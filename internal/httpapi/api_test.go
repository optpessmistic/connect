package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"surplustoken-connect/internal/configuration"
	"surplustoken-connect/internal/discovery"
	"surplustoken-connect/internal/gateway"
	"testing"
)

func TestAuthenticatedPlanApplyRestoreAndRedaction(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"panel-secret","refresh_token":"panel-refresh","expires_in":3600}}`)
		case "/api/v1/user/profile":
			fmt.Fprint(w, `{"code":0,"data":{"email":"fixture@example.com","username":"SuSu"}}`)
		case "/api/v1/groups/available":
			fmt.Fprint(w, `{"code":0,"data":[{"id":1,"name":"Claude","platform":"anthropic","status":"active"}]}`)
		case "/api/v1/keys":
			fmt.Fprint(w, `{"code":0,"data":{"items":[{"id":1,"name":"Connect","group_id":1,"key":"inference-secret","status":"active"}],"pages":1}}`)
		case "/api/v1/keys/1":
			fmt.Fprint(w, `{"code":0,"data":{"id":1,"name":"Connect","group_id":1,"key":"inference-secret","status":"active"}}`)
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer inference-secret" {
				t.Error("panel token used for inference")
			}
			fmt.Fprint(w, `{"data":[{"id":"model-x"}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "settings.json")
	engine, err := configuration.New(filepath.Join(root, "backups"), map[string]string{"claude": path, "opencode": filepath.Join(root, "opencode.jsonc")})
	if err != nil {
		t.Fatal(err)
	}
	g := gateway.New()
	g.Base = remote.URL
	api := New(g, engine)
	engine.Discover = func() map[string]discovery.Result { return map[string]discovery.Result{} }
	call := func(action, session string, in any) (int, []byte) {
		b, _ := json.Marshal(in)
		req := httptest.NewRequest("POST", "/connect/"+action, bytes.NewReader(b))
		req.Header.Set("X-Connect-Request", "1")
		req.Header.Set("X-Connect-Session", session)
		req.Header.Set("Origin", "http://127.0.0.1:5173")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		return w.Code, w.Body.Bytes()
	}
	code, body := call("login", "", map[string]string{"email": "fixture@example.com", "password": "password"})
	if code != 200 {
		t.Fatalf("login %d", code)
	}
	if bytes.Contains(body, []byte("panel-secret")) || bytes.Contains(body, []byte("inference-secret")) {
		t.Fatal("credential leaked to frontend")
	}
	var login struct {
		Session string `json:"session"`
	}
	if err = json.Unmarshal(body, &login); err != nil {
		t.Fatal(err)
	}
	code, _ = call("plan", login.Session, map[string]any{"client": "codex", "key_id": 1, "model": "model-x", "mode": "permanent"})
	if code == 200 {
		t.Fatal("Codex write allowed")
	}
	code, body = call("plan", login.Session, map[string]any{"client": "claude", "key_id": 1, "model": "model-x", "mode": "permanent"})
	if code != 200 {
		t.Fatalf("plan %d: %s", code, body)
	}
	if bytes.Contains(body, []byte("inference-secret")) {
		t.Fatal("plan leaked key")
	}
	var p configuration.Plan
	json.Unmarshal(body, &p)
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preview wrote target")
	}
	code, body = call("apply", login.Session, map[string]string{"plan_id": p.ID})
	if code != 200 {
		t.Fatalf("apply %d", code)
	}
	var record configuration.Record
	json.Unmarshal(body, &record)
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte("inference-secret")) {
		t.Fatal("credential not written to client")
	}
	code, body = call("history", login.Session, map[string]string{})
	if code != 200 || bytes.Contains(body, []byte("inference-secret")) {
		t.Fatal("history redaction failed")
	}
	code, body = call("restore", login.Session, map[string]string{"record_id": record.ID})
	if code != 200 {
		t.Fatalf("restore %d", code)
	}
	json.Unmarshal(body, &p)
	code, _ = call("apply", login.Session, map[string]string{"plan_id": p.ID})
	if code != 200 {
		t.Fatal("restore apply failed")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("restore did not remove managed new file")
	}
	code, body = call("plan", login.Session, map[string]any{"client": "opencode", "key_id": 1, "mode": "permanent"})
	if code != 200 || bytes.Contains(body, []byte("inference-secret")) {
		t.Fatal("OpenCode template without default model failed or leaked credential")
	}
	json.Unmarshal(body, &p)
	for _, change := range p.Changes {
		if change.Field == "model" || strings.HasPrefix(change.Field, "agents.") {
			t.Fatal("template changed model or agent choices")
		}
	}
	code, _ = call("history", "", map[string]string{})
	if code != 401 {
		t.Fatal("anonymous history exposed")
	}
}
func TestCrossOriginAndMissingHeaderRejected(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := configuration.New(filepath.Join(root, "backups"), map[string]string{})
	api := New(gateway.New(), engine)
	for _, origin := range []string{"https://attacker.example", ""} {
		req := httptest.NewRequest("POST", "/connect/login", strings.NewReader(`{}`))
		if origin != "" {
			req.Header.Set("Origin", origin)
			req.Header.Set("X-Connect-Request", "1")
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		if w.Code != 403 {
			t.Fatalf("invalid caller accepted %s", origin)
		}
	}
}

func TestNativeBridgeMatchesHTTPPipeline(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := configuration.New(filepath.Join(root, "backups"), map[string]string{})
	bridge := NewBridge(New(gateway.New(), engine))
	r, err := bridge.Request(context.Background(), "history", "", `{}`)
	if err != nil || r.Status != 401 {
		t.Fatal("native authorization differs from HTTP")
	}
	var payload map[string]string
	if json.Unmarshal(r.Data, &payload) != nil || payload["error"] != "请先登录" {
		t.Fatal("native result is not a JSON object")
	}
	if _, err = bridge.Request(context.Background(), "../history", "", `{}`); err == nil {
		t.Fatal("native path accepted")
	}
}
