//go:build windows

package discovery

import (
	"os/exec"
	"testing"
)

func TestProbeHidesConsoleWindow(t *testing.T) {
	cmd := exec.Command("fixture.exe", "--version")
	configureProbe(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("version probe must hide its console window")
	}
}
