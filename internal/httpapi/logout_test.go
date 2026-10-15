package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"surplustoken-connect/internal/configedit"
	"surplustoken-connect/internal/configuration"
	"surplustoken-connect/internal/gateway"
	"testing"
	"time"
)

type failingDeleteStore struct {
	memoryCredentialStore
	err error
}

func (s *failingDeleteStore) Delete() error {
	if s.err != nil {
		return s.err
	}
	return s.memoryCredentialStore.Delete()
}

func TestExpiredLogoutClearsSavedLoginAndPlans(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := configuration.New(filepath.Join(root, "backups"), map[string]string{"claude": filepath.Join(root, "settings.json")})
	if err != nil {
		t.Fatal(err)
	}
	api := New(gateway.New(), engine)
	store := &failingDeleteStore{memoryCredentialStore: memoryCredentialStore{token: "fixture-refresh"}}
	api.Store, api.savedID = store, "expired-session"
	api.sessions[api.savedID] = &localSession{remote: &gateway.Session{}, created: time.Now().Add(-9 * time.Hour), ready: true}
	plan, err := engine.Plan(api.savedID, "claude", []configedit.KV{{Path: "model", Value: "fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	call := func(action, id string) BridgeResponse {
		t.Helper()
		r, err := NewBridge(api).Request(context.Background(), action, id, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := call("history", "expired-session"); r.Status != 401 {
		t.Fatalf("expired history: %d", r.Status)
	}
	if _, err := engine.Apply("expired-session", plan.ID); err == nil {
		t.Fatal("expired plan retained")
	}
	if store.token == "" {
		t.Fatal("session expiry should not silently delete remembered login")
	}
	if r := call("logout", ""); r.Status != 401 {
		t.Fatal("anonymous logout accepted")
	}
	if r := call("logout", "another-session"); r.Status != 200 || store.token == "" {
		t.Fatal("unrelated logout deleted saved login")
	}
	store.err = errors.New("fixture credential store unavailable")
	if r := call("logout", "expired-session"); r.Status == 200 || api.savedID == "" {
		t.Fatal("credential delete failure reported success")
	}
	store.err = nil
	if r := call("logout", "expired-session"); r.Status != 200 || store.token != "" || api.savedID != "" {
		t.Fatal("expired logout failed to clear saved login")
	}
	if r := call("logout", "expired-session"); r.Status != 200 || store.deletes != 1 {
		t.Fatal("logout is not idempotent")
	}
	if r := call("resume", ""); r.Status != 200 {
		t.Fatal("resume after logout failed")
	} else {
		var data struct {
			Saved bool `json:"saved"`
		}
		if json.Unmarshal(r.Data, &data) != nil || data.Saved {
			t.Fatal("logout left a saved session")
		}
	}
}

func TestOlderLogoutCannotClearNewerSavedLogin(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := configuration.New(filepath.Join(root, "backups"), map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	api := New(gateway.New(), engine)
	store := &memoryCredentialStore{token: "newer-fixture-token"}
	api.Store, api.savedID = store, "new-session"
	api.sessions["old-session"] = &localSession{remote: &gateway.Session{}, created: time.Now(), ready: true}
	r, err := NewBridge(api).Request(context.Background(), "logout", "old-session", `{}`)
	if err != nil || r.Status != 200 || store.token != "newer-fixture-token" || api.savedID != "new-session" {
		t.Fatal("old logout affected new login")
	}
}
