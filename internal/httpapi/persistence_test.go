package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"surplustoken-connect/internal/configuration"
	"surplustoken-connect/internal/discovery"
	"surplustoken-connect/internal/gateway"
	"testing"
)

type memoryCredentialStore struct {
	token          string
	saves, deletes int
}

func (m *memoryCredentialStore) Load() (string, error) { return m.token, nil }
func (m *memoryCredentialStore) Save(s string) error   { m.token = s; m.saves++; return nil }
func (m *memoryCredentialStore) Delete() error         { m.token = ""; m.deletes++; return nil }
func TestRememberResumeRotationLogoutAndTransientFailure(t *testing.T) {
	status := 200
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"access-secret","refresh_token":"refresh-1","expires_in":3600}}`)
		case "/api/v1/auth/refresh":
			if status != 200 {
				w.WriteHeader(status)
				return
			}
			var body struct {
				Refresh string `json:"refresh_token"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Refresh != "refresh-1" {
				t.Error("saved refresh token not used")
			}
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"access-new","refresh_token":"refresh-2","expires_in":3600}}`)
		case "/api/v1/user/profile":
			fmt.Fprint(w, `{"code":0,"data":{"email":"fixture@example.com","username":"SuSu"}}`)
		case "/api/v1/groups/available":
			fmt.Fprint(w, `{"code":0,"data":[]}`)
		case "/api/v1/keys":
			fmt.Fprint(w, `{"code":0,"data":{"items":[],"pages":1}}`)
		case "/api/v1/auth/logout":
			fmt.Fprint(w, `{"code":0}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	engine, _ := configuration.New(filepath.Join(root, "backups"), map[string]string{})
	engine.Discover = func() map[string]discovery.Result { return map[string]discovery.Result{} }
	store := &memoryCredentialStore{}
	makeAPI := func() *API { g := gateway.New(); g.Base = remote.URL; a := New(g, engine); a.Store = store; return a }
	call := func(api *API, action, id, body string) BridgeResponse {
		response, err := NewBridge(api).Request(context.Background(), action, id, body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(response.Data), "refresh-") || strings.Contains(string(response.Data), "access-secret") {
			t.Fatal("secret leaked")
		}
		return response
	}
	first := makeAPI()
	login := call(first, "login", "", `{"email":"fixture@example.com","password":"password"}`)
	if login.Status != 200 || store.token != "refresh-1" {
		t.Fatal("login was not remembered")
	}
	second := makeAPI()
	resume := call(second, "resume", "", `{}`)
	if resume.Status != 200 || store.token != "refresh-2" {
		t.Fatal("restart did not rotate saved token")
	}
	var session struct {
		ID string `json:"session"`
	}
	json.Unmarshal(resume.Data, &session)
	if session.ID == "" {
		t.Fatal("resume missing local session")
	}
	logout := call(second, "logout", session.ID, `{}`)
	if logout.Status != 200 || store.token != "" || store.deletes != 1 {
		t.Fatal("logout did not clear credentials")
	}
	none := call(makeAPI(), "resume", "", `{}`)
	if string(none.Data) != "{\"saved\":false}\n" {
		t.Fatal("empty store auto signed in")
	}
	store.token = "refresh-1"
	status = 503
	offline := call(makeAPI(), "resume", "", `{}`)
	if offline.Status == 200 || store.token != "refresh-1" {
		t.Fatal("transient failure deleted saved session")
	}
	status = 401
	expired := call(makeAPI(), "resume", "", `{}`)
	if expired.Status != 200 || store.token != "" {
		t.Fatal("invalid refresh token retained")
	}
}
