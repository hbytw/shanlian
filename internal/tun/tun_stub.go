//go:build !linux

package tun

import (
	"errors"
	"fmt"
)

// file 在非 Linux 平台上的占位实现。
type file struct{}

func (f *file) read(b []byte) (int, error)  { return 0, errUnsupported }
func (f *file) write(b []byte) (int, error) { return 0, errUnsupported }
func (f *file) close() error                { return nil }

var errUnsupported = errors.New("TUN 仅在 Linux 上可用")

// Open 在非 Linux 平台上始终失败。
//
// SLX 的目标平台只有 Linux（OpenWrt / CentOS 7 / Ubuntu）。
// 保留这个 stub 是为了让开发机（Windows）也能 go build / go test。
func Open(name string, mtu int) (*Device, error) {
	return nil, fmt.Errorf("当前平台 %s 不支持 TUN，SLX 的网关角色只在 Linux 上可用", platform())
}

// Configure 在非 Linux 平台上是空操作。
func (d *Device) Configure(cidr string) error { return errUnsupported }

// ConfigureNoAddr 在非 Linux 平台上是空操作。
func (d *Device) ConfigureNoAddr() error { return errUnsupported }

// SetNonblock 在非 Linux 平台上是空操作。
func (d *Device) SetNonblock(on bool) error { return errUnsupported }

// IsNonblockError 在非 Linux 平台上恒为 false。
func IsNonblockError(err error) bool { return false }

func platform() string {
	return "non-linux"
}
