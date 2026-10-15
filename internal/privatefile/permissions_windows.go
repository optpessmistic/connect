//go:build windows

package privatefile

import (
	"os"

	"golang.org/x/sys/windows"
)

// POSIX modes do not restrict Windows readers. Protect the DACL from inherited
// permissions, granting only the current user and LocalSystem access. Do this
// before writing any secret bytes; filesystems without ACL support fail closed.
func restrict(path string, directory bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;" + flags + ";FA;;;" + user.User.Sid.String() + ")(A;" + flags + ";FA;;;SY)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

func Directory(path string) error { return restrict(path, true) }
func File(f *os.File) error       { return restrict(f.Name(), false) }
