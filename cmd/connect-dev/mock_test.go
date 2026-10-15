package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"surplustoken-connect/internal/gateway"
)

func callMock(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://fixture.invalid"+path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeMockKey(t *testing.T, response *httptest.ResponseRecorder) gateway.Key {
	t.Helper()
	var envelope struct {
		Data gateway.Key `json:"data"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Data.ID == 0 || envelope.Data.Value == "" {
		t.Fatalf("invalid key response: status=%d body=%s", response.Code, response.Body.String())
	}
	return envelope.Data
}

func TestMockCreatedKeysPersistAndUseTheirGroupsModels(t *testing.T) {
	handler := newMockGatewayHandler()
	for group := int64(1); group <= 2; group++ {
		body := fmt.Sprintf(`{"name":"Created key %d","group_id":%d}`, group, group)
		created := decodeMockKey(t, callMock(handler, http.MethodPost, "/api/v1/keys", "fixture-access", body))
		if created.ID != group+2 || created.GroupID != group || created.Status != "active" {
			t.Fatalf("created key has incorrect fields: %+v", created)
		}
		read := decodeMockKey(t, callMock(handler, http.MethodGet, fmt.Sprintf("/api/v1/keys/%d", created.ID), "fixture-access", ""))
		if read.ID != created.ID || read.Value != created.Value || read.Name != created.Name {
			t.Fatal("created key was not persisted for later lookup")
		}
		models := callMock(handler, http.MethodGet, "/v1/models", created.Value, "")
		want := "fixture-model"
		if group == 2 {
			want = "gpt-fixture"
		}
		var catalog struct {
			Data []struct{ ID string } `json:"data"`
		}
		if models.Code != http.StatusOK || json.Unmarshal(models.Body.Bytes(), &catalog) != nil || len(catalog.Data) != 1 || catalog.Data[0].ID != want {
			t.Fatalf("wrong catalog for group %d: %s", group, models.Body.String())
		}
	}
	list := callMock(handler, http.MethodGet, "/api/v1/keys", "fixture-access", "")
	var envelope struct {
		Data struct {
			Items []gateway.Key `json:"items"`
		} `json:"data"`
	}
	if json.Unmarshal(list.Body.Bytes(), &envelope) != nil || len(envelope.Data.Items) != 4 {
		t.Fatalf("created keys missing from list: %s", list.Body.String())
	}
	for _, token := range []string{"", "not-a-key", "fixture-access"} {
		if response := callMock(handler, http.MethodGet, "/v1/models", token, ""); response.Code != http.StatusUnauthorized {
			t.Fatal("unknown or panel credential was accepted for model access")
		}
	}
}

func TestMockRejectsInvalidCreationWithoutChangingList(t *testing.T) {
	handler := newMockGatewayHandler()
	for _, body := range []string{`{}`, `{"name":"missing-group"}`, `{"name":"wrong-group","group_id":99}`, `{"name":" ","group_id":1}`, `not-json`} {
		if response := callMock(handler, http.MethodPost, "/api/v1/keys", "fixture-access", body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid key creation accepted: %s", body)
		}
	}
	created := decodeMockKey(t, callMock(handler, http.MethodPost, "/api/v1/keys", "fixture-access", `{"name":"valid","group_id":1}`))
	if created.ID != 3 {
		t.Fatal("invalid creation mutated key state")
	}
	if response := callMock(handler, http.MethodDelete, "/api/v1/keys", "fixture-access", ""); response.Code != http.StatusMethodNotAllowed {
		t.Fatal("unsupported key mutation was accepted")
	}
	if response := callMock(handler, http.MethodPost, "/api/v1/keys", "", `{"name":"unauthorized","group_id":1}`); response.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated key creation was accepted")
	}
}

func TestMockConcurrentCreationKeepsUniqueKeys(t *testing.T) {
	handler := newMockGatewayHandler()
	const count = 16
	responses := make(chan *httptest.ResponseRecorder, count)
	var wait sync.WaitGroup
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses <- callMock(handler, http.MethodPost, "/api/v1/keys", "fixture-access", `{"name":"concurrent","group_id":2}`)
		}()
	}
	wait.Wait()
	close(responses)
	seen := map[int64]bool{}
	for response := range responses {
		key := decodeMockKey(t, response)
		if seen[key.ID] {
			t.Fatal("concurrent key creation reused an ID")
		}
		seen[key.ID] = true
	}
}
