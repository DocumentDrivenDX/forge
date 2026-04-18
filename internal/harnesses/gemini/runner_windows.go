//go:build windows

package gemini

import (
	osexec "os/exec"
	"syscall"
)

func setProcessGroupAttr(attr *syscall.SysProcAttr) {}

func killProcessGroup(cmd *osexec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
