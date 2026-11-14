//go:build !unix && !windows

package git

import "os/exec"

// detach does nothing where there are neither sessions nor process trees to
// end: git alone is killed when its context ends.
func detach(*exec.Cmd) {}

// start starts git.
func start(cmd *exec.Cmd) (func(failed bool), error) {
	return func(bool) {}, cmd.Start()
}

// reap does nothing where there are no process groups to kill.
func reap(*exec.Cmd) {}
