//go:build windows

package git

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createNoWindow starts a process with no console: windows.CREATE_NO_WINDOW.
const createNoWindow = 0x08000000

// detach starts git with no console, so an ssh asking for a key's passphrase
// has none to open (CONIN$) and fails rather than waits; and makes stopping git
// stop all it started too: Windows kills the one process it is given, so the
// ssh, remote helper or hook git ran would go on after orbit has moved on.
// taskkill /T ends the whole tree, while git is still there to name it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	cmd.Cancel = func() error {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		return cmd.Process.Kill()
	}
}

// start starts git in a job object, which everything git starts joins: Windows
// does not tie a process to the git that started it once git is gone, so a
// helper left holding git's output after git exits is found only that way. Git
// starts suspended and runs only once it is in the job, so nothing it starts
// can be outside it; if it cannot be put there, git does not run. The function
// returned, once git is done, ends what is left in the job when the run failed,
// as reap does elsewhere; after a success it leaves it running.
func start(cmd *exec.Cmd) (func(failed bool), error) {
	none := func(bool) {}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return none, fmt.Errorf("creating a job object for git: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	limit := func() error {
		_, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
		return err
	}
	if err := limit(); err != nil {
		_ = windows.CloseHandle(job)
		return none, fmt.Errorf("setting up a job object for git: %w", err)
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return none, err
	}
	if err := join(job, uint32(cmd.Process.Pid)); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = windows.CloseHandle(job)
		return none, fmt.Errorf("starting git in a job object: %w", err)
	}
	return func(failed bool) {
		if !failed {
			limits.BasicLimitInformation.LimitFlags = 0
			_ = limit()
		}
		_ = windows.CloseHandle(job)
	}, nil
}

// join puts the suspended process pid in job, then resumes its one thread.
func join(job windows.Handle, pid uint32) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	err = windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if err != nil {
		return err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		return err
	}
	return errors.New("git's thread was not found")
}

// reap does nothing more: the job ends what is left.
func reap(*exec.Cmd) {}
