//go:build windows

package configuration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"surplustoken-connect/internal/configedit"
)

func assertPrivateACL(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("secret file inherits parent permissions")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	text := sd.String()
	if strings.Count(text, "(A;") != 2 || strings.Count(text, ";FA;") != 2 || !strings.Contains(text, ";;;"+user.User.Sid.String()+")") || !strings.Contains(text, ";;;SY)") {
		t.Fatalf("unexpected secret-file ACL: %s", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() && strings.Count(text, "OICI") != 2 {
		t.Fatal("private directory does not protect child files")
	}
}

func TestWindowsPrivateConfigAndBackupACL(t *testing.T) {
	e, path := fixture(t)
	write(t, path, `{"theme":"original"}`)
	p, err := e.Plan("fixture", "claude", []configedit.KV{{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: "fixture-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Apply("fixture", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateACL(t, e.Store)
	assertPrivateACL(t, path)
	assertPrivateACL(t, filepath.Join(e.Store, r.ID+".json"))
	restore, err := e.Restore("fixture", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Apply("fixture", restore.ID); err != nil {
		t.Fatal(err)
	}
	assertPrivateACL(t, path)
}

func TestWindowsOccupiedFileDoesNotLoseOriginal(t *testing.T) {
	e, path := fixture(t)
	write(t, path, `{"model":"original"}`)
	p, err := e.Plan("fixture", "claude", []configedit.KV{{Path: "model", Value: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if handle != windows.InvalidHandle {
			windows.CloseHandle(handle)
		}
	}()
	if _, err = e.Apply("fixture", p.ID); err == nil || err.Error() != "配置写入失败，操作前备份已保存" {
		t.Fatalf("expected target replacement failure, got %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != `{"model":"original"}` {
		t.Fatal("occupied original was changed")
	}
	entries, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".connect-*"))
	if err != nil || len(entries) != 0 {
		t.Fatal("temporary secret file was not cleaned up")
	}
	if err = windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	handle = windows.InvalidHandle
	if _, err = e.Apply("fixture", p.ID); err != nil {
		t.Fatalf("retry after releasing file: %v", err)
	}
	raw, err = os.ReadFile(path)
	if err != nil || value(raw, "model").String() != "new" {
		t.Fatal("retry failed to update target")
	}
}
