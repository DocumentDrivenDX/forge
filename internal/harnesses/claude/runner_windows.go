//go:build windows

package claude

import (
	osexec "os/exec"
	"syscall"
)

// setProcessGroupAttr is a no-op on Windows; the runner relies on
// CommandContext's default kill behavior to terminate the child.
func setProcessGroupAttr(attr *syscall.SysProcAttr) {}

// killProcessGroup terminates the process directly on Windows. There is no
// portable POSIX-style process group, so we settle for the leader.
func killProcessGroup(cmd *osexec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
