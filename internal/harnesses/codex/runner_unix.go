//go:build !windows

package codex

import (
	osexec "os/exec"
	"syscall"
)

func setProcessGroupAttr(attr *syscall.SysProcAttr) {
	attr.Setpgid = true
}

func killProcessGroup(cmd *osexec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
}
