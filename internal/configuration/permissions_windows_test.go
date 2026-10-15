//go:build windows

package configuration

import (
	"fmt"
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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePrivateACL(sd, user.User.Sid, info.IsDir()); err != nil {
		t.Fatal(err)
	}
}

func validatePrivateACL(sd *windows.SECURITY_DESCRIPTOR, user *windows.SID, directory bool) error {
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("secret file inherits parent permissions")
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	// Windows may render a numeric SID as an SDDL alias (for example LA for
	// the built-in Administrator used by CI). Canonicalize both descriptors
	// before comparing their complete ACE lists. Keep the protected-DACL check
	// separate because filesystems may also set the auto-inherited control bit.
	expected, err := windows.SecurityDescriptorFromString("D:P(A;" + flags + ";FA;;;" + user.String() + ")(A;" + flags + ";FA;;;SY)")
	if err != nil {
		return err
	}
	actualText, expectedText := sd.String(), expected.String()
	actualStart, expectedStart := strings.Index(actualText, "("), strings.Index(expectedText, "(")
	if actualStart < 0 || expectedStart < 0 || actualText[actualStart:] != expectedText[expectedStart:] {
		return fmt.Errorf("unexpected secret-file ACL: %s", actualText)
	}
	return nil
}

func TestWindowsPrivateACLAcceptsEquivalentSIDRepresentations(t *testing.T) {
	// Resolve LA without changing the test process identity or machine accounts.
	owner, err := windows.SecurityDescriptorFromString("O:LA")
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := owner.Owner()
	if err != nil {
		t.Fatal(err)
	}
	for name, sddl := range map[string]string{
		"alias":   "D:P(A;OICI;FA;;;LA)(A;OICI;FA;;;SY)",
		"numeric": "D:P(A;OICI;FA;;;" + admin.String() + ")(A;OICI;FA;;;SY)",
	} {
		t.Run(name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := validatePrivateACL(sd, admin, true); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, sddl := range map[string]string{
		"extra_reader":  "D:P(A;OICI;FA;;;LA)(A;OICI;FA;;;SY)(A;OICI;FR;;;WD)",
		"wrong_user":    "D:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)",
		"read_only":     "D:P(A;OICI;FR;;;LA)(A;OICI;FA;;;SY)",
		"missing_oi":    "D:P(A;CI;FA;;;LA)(A;OICI;FA;;;SY)",
		"missing_ci":    "D:P(A;OI;FA;;;LA)(A;OICI;FA;;;SY)",
		"unprotected":   "D:(A;OICI;FA;;;LA)(A;OICI;FA;;;SY)",
		"missing_ace":   "D:P(A;OICI;FA;;;LA)",
		"duplicate_ace": "D:P(A;OICI;FA;;;LA)(A;OICI;FA;;;LA)",
	} {
		t.Run(name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(sddl)
			if err != nil {
				t.Fatal(err)
			}
			if err := validatePrivateACL(sd, admin, true); err == nil {
				t.Fatal("invalid private ACL was accepted")
			}
		})
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
