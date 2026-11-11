//go:build linux

package mesh

import "golang.org/x/sys/unix"

// setSocketOptions 为发现套接字打开必要的选项。
//
// SO_BROADCAST：广播发现的前提，Go 默认不开。
// SO_REUSEADDR：守护进程重启时能立刻重新绑定端口，否则要等内核回收。
func setSocketOptions(fd uintptr) error {
	if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1); err != nil {
		return err
	}
	return unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
}
