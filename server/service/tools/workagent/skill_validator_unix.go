//go:build !windows

package workagent

import (
	"os"
	"os/exec"
	"syscall"
)

// configureProcessGroup makes the child the leader of a fresh process
// group so killProcessGroup can SIGKILL the whole tree on timeout.
// Without it, validatorTimeout's CommandContext only signals python3
// itself; any subprocess python spawns (subprocess.Popen, multiproc
// pool worker, etc.) would orphan and keep running until the host
// reaped them.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup sends SIGKILL to the entire process group. Safe
// to call after a successful exit too — the pgid is gone and Kill
// returns ESRCH which we discard.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// isOwnedByCurrentEUID reports whether the FileInfo belongs to the
// effective UID of this process. Returns false if the platform stat
// payload isn't a *syscall.Stat_t (defensive — should always be on
// !windows builds). Used by isSafeOwnedScript so the cache-hit path
// matches the docstring's UID guarantee. Without this check, a tmp
// reaper that yanked our file followed by a cross-user create at
// the same predictable path could land us reusing a foreign-owned
// file — readable or not, that violates the documented contract.
func isOwnedByCurrentEUID(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == os.Geteuid()
}
