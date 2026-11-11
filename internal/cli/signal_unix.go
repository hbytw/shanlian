//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// refreshSignal 是「立刻自查」用的信号。
func refreshSignal() os.Signal { return syscall.SIGUSR1 }

// signalUSR1 给指定进程发 SIGUSR1，让它立刻自查一次分流规则。
func signalUSR1(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGUSR1)
}
