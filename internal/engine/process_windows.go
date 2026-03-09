//go:build windows

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcGroup 在 Windows 上设置子进程使用新的进程组
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// killProcessGroup 在 Windows 上终止进程
func killProcessGroup(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
