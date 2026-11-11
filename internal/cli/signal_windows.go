//go:build windows

package cli

import (
	"fmt"
	"os"
)

// refreshSignal 在 Windows 上没有 SIGUSR1，用一个不会触发的占位信号。
func refreshSignal() os.Signal { return os.Interrupt }

// signalUSR1 在 Windows 上无对应语义。
//
// slx 的运行时目标只有 Linux（OpenWrt / CentOS / Ubuntu / Debian），
// 这个桩只是为了开发机（Windows）上 `go build ./...` 能通过。
func signalUSR1(pid int) error {
	return fmt.Errorf("Windows 不支持 SIGUSR1，无法通知进程 %d", pid)
}
