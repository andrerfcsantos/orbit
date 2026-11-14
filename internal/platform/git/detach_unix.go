//go:build unix

package git

import (
	"os/exec"
	"syscall"
)

// detach runs git in a session of its own, with no controlling terminal: ssh
// cannot ask for a passphrase on it, and Ctrl-C at the terminal reaches orbit,
// which decides what to stop. Stopping interrupts git's whole process group, so
// the ssh or remote helper it started goes with it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	}
}

// start starts git; its process group is what reap ends.
func start(cmd *exec.Cmd) (func(failed bool), error) {
	return func(bool) {}, cmd.Start()
}

// reap kills what is left of a stopped git's process group: past WaitDelay,
// exec kills git alone, and a hook or helper that ignored the interrupt would
// go on changing files after orbit has moved on. The group outlives git while
// any of it runs, so its id is not another's.
func reap(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
