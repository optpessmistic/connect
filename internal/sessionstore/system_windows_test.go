//go:build windows

package sessionstore

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Opt in only for an interactive Windows user with a Credential Manager store.
func TestWindowsCredentialStoreRoundTrip(t *testing.T) {
	if os.Getenv("CONNECT_TEST_CREDENTIAL_STORE") != "1" {
		t.Skip("set CONNECT_TEST_CREDENTIAL_STORE=1 for the disposable OS-store integration test")
	}
	store := System{serviceName: fmt.Sprintf("com.surplustoken.connect.test.%d.%d", os.Getpid(), time.Now().UnixNano()), accountName: "fixture"}
	t.Cleanup(func() {
		if err := store.Delete(); err != nil {
			t.Error(err)
		}
	})
	if got, err := store.Load(); err != nil || got != "" {
		t.Fatal("unexpected initial credential")
	}
	if err := store.Save("fixture-refresh-one"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || got != "fixture-refresh-one" {
		t.Fatal("credential round trip failed")
	}
	if err := store.Save("fixture-refresh-two"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || got != "fixture-refresh-two" {
		t.Fatal("credential rotation failed")
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || got != "" {
		t.Fatal("credential deletion failed")
	}
	if err := store.Delete(); err != nil {
		t.Fatal("credential deletion is not idempotent")
	}
}
