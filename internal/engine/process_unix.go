//go:build !windows

package engine

import (
	"os/exec"
	"syscall"
)

// setProcGroup 设置子进程使用独立进程组
// 这样可以通过 Kill(-pid) 杀掉整个进程组（含子进程）
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 杀掉整个进程组
func killProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}
