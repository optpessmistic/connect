package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOptionsRequireFixtureForMockAndLoopbackAddress(t *testing.T) {
	for _, args := range [][]string{
		{"--mock"},
		{"--listen", "0.0.0.0:0"},
		{"--listen", "[::]:0"},
		{"--listen", "192.0.2.1:43129"},
		{"--listen", "localhost:0"},
		{"--listen", ":0"},
		{"--listen", "127.0.0.1:-1"},
		{"--listen", "127.0.0.1:65536"},
		{"--listen", "127.0.0.1:http"},
		{"unexpected"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := parseOptions(args, io.Discard); err == nil {
				t.Fatal("unsafe or invalid options accepted")
			}
		})
	}
	for _, address := range []string{"127.0.0.1:0", "127.0.0.1:43129", "[::1]:0"} {
		if _, err := parseOptions([]string{"--mock", "--fixture-dir", "fixture", "--listen", address}, io.Discard); err != nil {
			t.Fatalf("valid loopback options rejected: %v", err)
		}
	}
}

func TestFixtureEngineDoesNotProbeRealClients(t *testing.T) {
	engine, root, err := fixtureEngine(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(root) || engine.Discover == nil {
		t.Fatal("fixture must have an absolute root and an isolated discovery provider")
	}
	for _, client := range engine.Detect() {
		if client.Installed || client.Detection.Executable != "" {
			t.Fatal("fixture exposed a real client installation")
		}
	}
	for _, path := range engine.Paths {
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			t.Fatal("fixture path escaped its root")
		}
	}
}

type readyWriter struct{ messages chan string }

func (w readyWriter) Write(p []byte) (int, error) {
	w.messages <- string(p)
	return len(p), nil
}

func TestRunReportsActualRandomPortAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	out := readyWriter{messages: make(chan string, 1)}
	fixture := t.TempDir()
	go func() {
		done <- run(ctx, []string{"--mock", "--fixture-dir", fixture, "--listen", "127.0.0.1:0"}, out)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("development server did not stop after cancellation")
		}
	})
	var ready readyMessage
	select {
	case line := <-out.messages:
		const prefix = "CONNECT_DEV_READY "
		if !strings.HasPrefix(line, prefix) || json.Unmarshal([]byte(strings.TrimPrefix(line, prefix)), &ready) != nil {
			t.Fatalf("invalid readiness line: %s", line)
		}
	case err := <-done:
		done <- err // The cleanup also checks the final result.
		t.Fatalf("development server stopped before readiness: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("development server did not report readiness")
	}
	u, err := url.Parse(ready.Address)
	if err != nil || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "0" || u.Port() == "" || !ready.Mock || !filepath.IsAbs(ready.FixtureDir) {
		t.Fatalf("invalid readiness payload: %+v", ready)
	}
	request, err := http.NewRequest(http.MethodPost, ready.Address+"/connect/login", strings.NewReader(`{"email":"fixture@example.test","password":"fixture-only"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Connect-Request", "1")
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var login struct {
		Fixture bool   `json:"fixture"`
		Session string `json:"session"`
	}
	if err := json.NewDecoder(response.Body).Decode(&login); err != nil || response.StatusCode != http.StatusOK || !login.Fixture || login.Session == "" {
		t.Fatalf("isolated offline login failed: status=%d, response=%+v, error=%v", response.StatusCode, login, err)
	}
}
