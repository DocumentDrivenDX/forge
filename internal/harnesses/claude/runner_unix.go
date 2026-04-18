//go:build !windows

package claude

import (
	osexec "os/exec"
	"syscall"
)

// setProcessGroupAttr puts the child process in its own process group so
// signals sent to the group don't leak back into the parent.
func setProcessGroupAttr(attr *syscall.SysProcAttr) {
	attr.Setpgid = true
}

// killProcessGroup signals SIGTERM to the entire process group of cmd.
// Best-effort: missing process / already-exited cases are ignored. This is
// the orphan-reaping path used on ctx.Done().
func killProcessGroup(cmd *osexec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// Fall back to direct signal on the leader.
		_ = cmd.Process.Signal(syscall.SIGTERM)
		return
	}
	// Negative pid -> entire process group.
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
}
