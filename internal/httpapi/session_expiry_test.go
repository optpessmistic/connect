package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"surplustoken-connect/internal/configedit"
	"surplustoken-connect/internal/configuration"
	"surplustoken-connect/internal/discovery"
	"surplustoken-connect/internal/gateway"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureResponse(status int, body string) (*http.Response, error) {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

type authFixture struct {
	panelStatus   atomic.Int32
	refreshStatus atomic.Int32
	modelStatus   atomic.Int32
	offline       atomic.Bool
}

func (f *authFixture) request(r *http.Request) (*http.Response, error) {
	if f.offline.Load() {
		return nil, errors.New("fixture network unavailable")
	}
	switch r.URL.Path {
	case "/api/v1/auth/login":
		return fixtureResponse(200, `{"code":0,"data":{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600}}`)
	case "/api/v1/auth/refresh":
		if status := int(f.refreshStatus.Load()); status != 0 {
			return fixtureResponse(status, `{}`)
		}
		return fixtureResponse(200, `{"code":0,"data":{"access_token":"fixture-rotated-access","refresh_token":"fixture-rotated-refresh","expires_in":3600}}`)
	case "/api/v1/user/profile":
		if status := int(f.panelStatus.Load()); status != 0 {
			return fixtureResponse(status, `{}`)
		}
		return fixtureResponse(200, `{"code":0,"data":{"email":"fixture@example.com","username":"fixture"}}`)
	case "/api/v1/groups/available":
		return fixtureResponse(200, `{"code":0,"data":[{"id":1,"name":"fixture","platform":"anthropic","status":"active"}]}`)
	case "/api/v1/keys":
		return fixtureResponse(200, `{"code":0,"data":{"items":[],"pages":1}}`)
	case "/api/v1/keys/1":
		return fixtureResponse(200, `{"code":0,"data":{"id":1,"group_id":1,"key":"fixture-inference-key","status":"active"}}`)
	case "/v1/models":
		if status := int(f.modelStatus.Load()); status != 0 {
			return fixtureResponse(status, `{}`)
		}
		return fixtureResponse(200, `{"data":[{"id":"fixture-model"}]}`)
	case "/api/v1/auth/logout":
		return fixtureResponse(200, `{"code":0,"data":{}}`)
	default:
		return fixtureResponse(404, `{}`)
	}
}

func authFixtureAPI(t *testing.T, transport fixtureTransport) (*API, *failingDeleteStore) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := configuration.New(filepath.Join(root, "backups"), map[string]string{"claude": filepath.Join(root, "settings.json")})
	if err != nil {
		t.Fatal(err)
	}
	engine.Discover = func() map[string]discovery.Result { return map[string]discovery.Result{} }
	client := gateway.New()
	client.Base = "https://fixture.invalid"
	client.HTTP.Transport = transport
	a := New(client, engine)
	store := &failingDeleteStore{}
	a.Store = store
	return a, store
}

func authCall(t *testing.T, a *API, action, id, body string) BridgeResponse {
	t.Helper()
	response, err := NewBridge(a).Request(context.Background(), action, id, body)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func authLogin(t *testing.T, a *API) string {
	t.Helper()
	response := authCall(t, a, "login", "", `{"email":"fixture@example.com","password":"fixture-password"}`)
	return authSessionID(t, response)
}

func authSessionID(t *testing.T, response BridgeResponse) string {
	t.Helper()
	var data struct{ Session string }
	if response.Status != 200 || json.Unmarshal(response.Data, &data) != nil || data.Session == "" {
		t.Fatalf("fixture login: %d %s", response.Status, response.Data)
	}
	return data.Session
}

type asyncAuthResult struct {
	response BridgeResponse
	err      error
}

func asyncAuthCall(a *API, action, id, body string) <-chan asyncAuthResult {
	result := make(chan asyncAuthResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := NewBridge(a).Request(ctx, action, id, body)
		result <- asyncAuthResult{response, err}
	}()
	return result
}

func awaitAuth(t *testing.T, result <-chan asyncAuthResult) BridgeResponse {
	t.Helper()
	select {
	case finished := <-result:
		if finished.err != nil {
			t.Fatal(finished.err)
		}
		return finished.response
	case <-time.After(6 * time.Second):
		t.Fatal("authentication fixture did not finish")
		return BridgeResponse{}
	}
}

func TestPanelAuthenticationFailureExpiresLocalSessionAndPlans(t *testing.T) {
	for _, refreshStatus := range []int32{401, 403, 0} {
		t.Run(http.StatusText(int(refreshStatus)), func(t *testing.T) {
			fixture := &authFixture{}
			a, store := authFixtureAPI(t, fixture.request)
			id := authLogin(t, a)
			plan, err := a.Engine.Plan(id, "claude", []configedit.KV{{Path: "model", Value: "fixture"}})
			if err != nil {
				t.Fatal(err)
			}
			fixture.panelStatus.Store(401)
			fixture.refreshStatus.Store(refreshStatus)
			response := authCall(t, a, "session", id, `{}`)
			if response.Status != 401 || a.session(id) != nil || store.token != "" || a.savedID != "" {
				t.Fatalf("panel revocation did not expire local login: %d %s", response.Status, response.Data)
			}
			if _, err := a.Engine.Apply(id, plan.ID); err == nil || !strings.Contains(err.Error(), "计划已失效") {
				t.Fatalf("revoked session plan remains usable: %v", err)
			}
		})
	}
}

func TestInferenceKeyPermissionAndNetworkErrorsKeepPanelSession(t *testing.T) {
	fixture := &authFixture{}
	a, store := authFixtureAPI(t, fixture.request)
	id := authLogin(t, a)
	fixture.modelStatus.Store(401)
	response := authCall(t, a, "models", id, `{"key_id":1}`)
	if response.Status != 400 || a.session(id) == nil || store.token == "" || store.deletes != 0 {
		t.Fatal("inference Key 401 revoked the panel session")
	}
	if !strings.Contains(string(response.Data), "API Key") || strings.Contains(string(response.Data), "重新登录") {
		t.Fatal("inference Key failure was presented as panel login expiry")
	}
	fixture.panelStatus.Store(403)
	if response := authCall(t, a, "session", id, `{}`); response.Status != 400 || a.session(id) == nil {
		t.Fatal("endpoint permission failure revoked the panel session")
	}
	fixture.panelStatus.Store(0)
	fixture.offline.Store(true)
	if response := authCall(t, a, "session", id, `{}`); response.Status != 400 || a.session(id) == nil || store.deletes != 0 {
		t.Fatal("network failure revoked the panel session")
	}
	fixture.offline.Store(false)
	fixture.panelStatus.Store(401)
	fixture.refreshStatus.Store(503)
	if response := authCall(t, a, "session", id, `{}`); response.Status != 400 || a.session(id) == nil || store.deletes != 0 {
		t.Fatal("temporary refresh failure revoked the panel session")
	}
	fixture.panelStatus.Store(0)
	fixture.refreshStatus.Store(0)
	if response := authCall(t, a, "session", id, `{}`); response.Status != 200 {
		t.Fatal("session could not recover after a transient error")
	}
}

func TestExpiredCredentialCleanupFailureCanBeRetried(t *testing.T) {
	for _, retry := range []string{"logout", "resume"} {
		t.Run(retry, func(t *testing.T) {
			fixture := &authFixture{}
			a, store := authFixtureAPI(t, fixture.request)
			id := authLogin(t, a)
			store.err = errors.New("fixture keychain locked")
			fixture.panelStatus.Store(401)
			fixture.refreshStatus.Store(401)
			response := authCall(t, a, "session", id, `{}`)
			if response.Status != 401 || !strings.Contains(string(response.Data), `"credential_cleanup_required":true`) || !a.clearPending || store.token == "" || a.session(id) != nil {
				t.Fatalf("cleanup failure was not explicit and retryable: %s", response.Data)
			}
			store.err = nil
			if response := authCall(t, a, retry, id, `{}`); response.Status != 200 || store.token != "" || a.clearPending || a.savedID != "" {
				t.Fatalf("cleanup retry failed: %d %s", response.Status, response.Data)
			}
		})
	}
}

func TestOlderSessionAuthenticationFailureCannotDeleteNewLogin(t *testing.T) {
	fixture := &authFixture{}
	a, store := authFixtureAPI(t, fixture.request)
	oldID := authLogin(t, a)
	newID := authLogin(t, a)
	fixture.panelStatus.Store(401)
	fixture.refreshStatus.Store(401)
	if response := authCall(t, a, "session", oldID, `{}`); response.Status != 401 {
		t.Fatal("old session did not expire")
	}
	if a.savedID != newID || a.session(newID) == nil || store.token == "" || store.deletes != 0 {
		t.Fatal("older session failure deleted newer login credentials")
	}
}

func TestConcurrentResumeFailureCannotDeleteLaterLogin(t *testing.T) {
	fixture := &authFixture{}
	resumeStarted, releaseResume := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseResume) })
	loginEntered := make(chan struct{}, 1)
	a, store := authFixtureAPI(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/auth/refresh" {
			close(resumeStarted)
			<-releaseResume
			return fixtureResponse(401, `{}`)
		}
		if r.URL.Path == "/api/v1/auth/login" {
			loginEntered <- struct{}{}
		}
		return fixture.request(r)
	})
	store.token = "fixture-old-refresh"
	resumeDone := asyncAuthCall(a, "resume", "", `{}`)
	<-resumeStarted
	loginDone := asyncAuthCall(a, "login", "", `{"email":"fixture@example.com","password":"fixture-password"}`)
	var id string
	select {
	case <-loginEntered:
		// This ordering exposed the original race: login finished while an old
		// restore request was still waiting to delete the saved credential.
		id = authSessionID(t, awaitAuth(t, loginDone))
	case <-time.After(100 * time.Millisecond):
		// Authentication lifecycle operations are now serialized.
	}
	releaseOnce.Do(func() { close(releaseResume) })
	if response := awaitAuth(t, resumeDone); response.Status != 200 {
		t.Fatalf("expired saved login result: %d", response.Status)
	}
	if id == "" {
		id = authSessionID(t, awaitAuth(t, loginDone))
	}
	if a.savedID != id || store.token != "fixture-refresh" || a.session(id) == nil {
		t.Fatal("old resume failure deleted a later successful login")
	}
}

func TestStaleResumeRejectionPreservesConcurrentTokenRotation(t *testing.T) {
	fixture := &authFixture{}
	resumeStarted, releaseResume := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseResume) })
	var refreshCalls atomic.Int32
	var rejectOldAccess atomic.Bool
	a, store := authFixtureAPI(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/auth/refresh" && refreshCalls.Add(1) == 1 {
			close(resumeStarted)
			<-releaseResume
			return fixtureResponse(401, `{}`)
		}
		if r.URL.Path == "/api/v1/user/profile" && rejectOldAccess.Load() && r.Header.Get("Authorization") == "Bearer fixture-access" {
			return fixtureResponse(401, `{}`)
		}
		return fixture.request(r)
	})
	id := authLogin(t, a)
	local := a.session(id)
	a.mu.Lock()
	local.created = time.Now().Add(-9 * time.Hour)
	a.mu.Unlock()
	rejectOldAccess.Store(true)
	resumeDone := asyncAuthCall(a, "resume", "", `{}`)
	<-resumeStarted
	// A request that already captured the old session can still finish its
	// refresh while startup restoration is waiting on an older stored token.
	if _, err := local.remote.Profile(context.Background()); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(releaseResume) })
	response := awaitAuth(t, resumeDone)
	if response.Status != 400 || !strings.Contains(string(response.Data), "已更新") || store.token != "fixture-rotated-refresh" || store.deletes != 0 {
		t.Fatalf("old restore response deleted a rotated token: %d %s", response.Status, response.Data)
	}
}
