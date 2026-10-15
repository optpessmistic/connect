package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"

	"surplustoken-connect/internal/gateway"
)

// Explicit offline fixture only. It is not compiled into the Wails app.
func mockGateway() *httptest.Server { return httptest.NewServer(newMockGatewayHandler()) }

func newMockGatewayHandler() http.Handler {
	var mu sync.Mutex
	keys := []gateway.Key{
		{ID: 1, Name: "离线测试 Key", GroupID: 1, Value: "fixture-not-a-real-key", Status: "active"},
		{ID: 2, Name: "OpenAI 测试 Key", GroupID: 2, Value: "fixture-openai-key", Status: "active"},
	}
	groups := []gateway.Group{
		{ID: 1, Name: "离线测试分组", Platform: "anthropic", Status: "active", AllowMessages: true},
		{ID: 2, Name: "OpenAI 测试分组", Platform: "openai", Status: "active"},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fail := func(status int, code string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
		}
		method := func(want string) bool {
			if r.Method == want {
				return true
			}
			w.Header().Set("Allow", want)
			fail(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
			return false
		}
		respond := func(data any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
		}
		if r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/refresh" {
			if !method(http.MethodPost) {
				return
			}
			respond(map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh", "expires_in": 3600})
			return
		}
		if r.URL.Path == "/v1/models" {
			if !method(http.MethodGet) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, key := range keys {
				if key.Status == "active" && r.Header.Get("Authorization") == "Bearer "+key.Value {
					model := "fixture-model"
					if key.GroupID == 2 {
						model = "gpt-fixture"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": model}}})
					return
				}
			}
			fail(http.StatusUnauthorized, "INVALID_KEY")
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-access" {
			fail(http.StatusUnauthorized, "UNAUTHORIZED")
			return
		}
		switch r.URL.Path {
		case "/api/v1/auth/logout":
			if method(http.MethodPost) {
				respond(map[string]bool{"ok": true})
			}
		case "/api/v1/user/profile":
			if method(http.MethodGet) {
				respond(map[string]string{"email": "preview@example.com", "username": "测试用户"})
			}
		case "/api/v1/groups/available":
			if method(http.MethodGet) {
				respond(groups)
			}
		case "/api/v1/keys":
			mu.Lock()
			defer mu.Unlock()
			switch r.Method {
			case http.MethodGet:
				respond(map[string]any{"items": keys, "pages": 1})
			case http.MethodPost:
				var input struct {
					Name    string `json:"name"`
					GroupID int64  `json:"group_id"`
				}
				decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
				decoder.DisallowUnknownFields()
				if decoder.Decode(&input) != nil || strings.TrimSpace(input.Name) == "" || len(input.Name) > 200 || input.GroupID < 1 || input.GroupID > 2 {
					fail(http.StatusBadRequest, "INVALID_KEY_INPUT")
					return
				}
				id := int64(len(keys) + 1)
				key := gateway.Key{ID: id, Name: input.Name, GroupID: input.GroupID, Value: fmt.Sprintf("fixture-key-%d", id), Status: "active"}
				keys = append(keys, key)
				respond(key)
			default:
				w.Header().Set("Allow", "GET, POST")
				fail(http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
			}
		default:
			if !strings.HasPrefix(r.URL.Path, "/api/v1/keys/") {
				fail(http.StatusNotFound, "NOT_FOUND")
				return
			}
			if !method(http.MethodGet) {
				return
			}
			id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/v1/keys/"), 10, 64)
			if err == nil {
				mu.Lock()
				defer mu.Unlock()
				for _, key := range keys {
					if key.ID == id {
						respond(key)
						return
					}
				}
			}
			fail(http.StatusNotFound, "KEY_NOT_FOUND")
		}
	})
}
