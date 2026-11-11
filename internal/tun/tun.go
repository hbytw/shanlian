// Package tun 封装 Linux TUN 虚拟网卡。
//
// 只有网关角色需要 TUN：它把"本该发往境外"的 IP 包从内核手里接过来，
// 交给隧道；出口节点虽然也要 TUN，但那是为了把回程包送回内核做 NAT。
//
// 目标平台只有 Linux，非 Linux 提供空实现以便在开发机上编译测试。
package tun

// 设备参数。
const (
	// DefaultName 是默认设备名。带上 slx 前缀便于识别与清理。
	DefaultName = "slx0"
	// DefaultMTU 是默认 MTU。
	//
	// 取 1380 的考虑：外层要装 IP(20) + UDP(8) + SLX 帧头(4)，
	// 再叠加 ZeroTier 自身的封装，给链路上可能存在的 PPPoE(8) 等留余量。
	// 宁可小 20 字节，也不要让大包在链路上被分片 —— 分片对吞吐的伤害远大于 MTU 损失。
	DefaultMTU = 1380
	// MaxPacket 是单个 IP 包的上限（含 IPv6）。
	MaxPacket = 65535
)

// Device 是一个已打开并可读写的 TUN 设备。
type Device struct {
	f    *file
	name string
	mtu  int
}

// Name 返回设备名。
func (d *Device) Name() string { return d.name }

// MTU 返回设备 MTU。
func (d *Device) MTU() int { return d.mtu }

// Read 读取一个 IP 包到 buf，返回包长度。
//
// TUN 读是阻塞的，调用方应放在独立 goroutine 里。
func (d *Device) Read(buf []byte) (int, error) { return d.f.read(buf) }

// Write 写入一个 IP 包。
func (d *Device) Write(pkt []byte) (int, error) { return d.f.write(pkt) }

// Close 关闭设备。
func (d *Device) Close() error { return d.f.close() }
