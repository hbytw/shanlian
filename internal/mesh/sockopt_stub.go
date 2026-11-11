//go:build !linux

package mesh

// setSocketOptions 在非 Linux 平台上什么都不做。
//
// SLX 的目标平台只有 Linux（OpenWrt / CentOS 7 / Ubuntu）。
// 保留这个空实现是为了让开发机（Windows）也能 `go build` 和 `go test`，
// 便于在没有 Linux 环境时跑单元测试。
func setSocketOptions(fd uintptr) error { return nil }
