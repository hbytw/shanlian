//go:build linux

package tun

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// file 是跨平台文件句柄的最小抽象，便于 stub 实现。
type file struct {
	f *os.File
}

func (f *file) read(b []byte) (int, error)  { return f.f.Read(b) }
func (f *file) write(b []byte) (int, error) { return f.f.Write(b) }
func (f *file) close() error                { return f.f.Close() }

// Open 创建（或附加到已有的）TUN 设备。
//
// name 为空时使用 DefaultName；若同名设备已被占用，内核会返回 EBUSY，
// 这时我们复用已存在的设备而不是失败 —— 便于守护进程重启后快速恢复。
func Open(name string, mtu int) (*Device, error) {
	if name == "" {
		name = DefaultName
	}
	if mtu <= 0 {
		mtu = DefaultMTU
	}

	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("找不到 /dev/net/tun —— 内核未加载 tun 模块\n" +
				"提示: OpenWrt 执行 opkg install kmod-tun；其它发行版执行 modprobe tun")
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("/dev/net/tun 权限不足 —— 需要 root 或 CAP_NET_ADMIN")
		}
		return nil, fmt.Errorf("打开 /dev/net/tun 失败: %w", err)
	}

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("构造 ifreq 失败: %w", err)
	}
	// IFF_TUN     : 三层设备（不带以太网头）
	// IFF_NO_PI   : 不附加 4 字节的 packet info 头
	//               有 PI 头会让每个包多 4 字节且需要自己剥离，纯属负担
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)

	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("创建 TUN 设备 %q 失败: %w\n提示: 可能已有同名设备被其它程序占用", name, err)
	}

	actual := ifr.Name()
	dev := &Device{
		f:    &file{f: os.NewFile(uintptr(fd), "/dev/net/tun")},
		name: actual,
		mtu:  mtu,
	}
	return dev, nil
}

// Configure 配置设备地址、MTU 并启用。
//
// 用 `ip` 命令而不是自己发 netlink：代码量小一个数量级，
// 且 `ip` 在 OpenWrt(ip-full)、CentOS 7、Ubuntu 上都是标配。
// 缺失时 doctor 会给出准确的安装提示。
func (d *Device) Configure(cidr string) error {
	if _, err := exec.LookPath("ip"); err != nil {
		return fmt.Errorf("找不到 ip 命令，无法配置网卡\n" +
			"提示: OpenWrt 执行 opkg install ip-full；CentOS 执行 yum install iproute")
	}

	steps := [][]string{
		{"link", "set", "dev", d.name, "mtu", itoa(d.mtu)},
		{"addr", "add", cidr, "dev", d.name},
		{"link", "set", "dev", d.name, "up"},
	}
	for _, args := range steps {
		cmd := exec.Command("ip", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			// `ip addr add` 重复添加会报 "File exists"，属于幂等场景，忽略
			if args[0] == "addr" && containsAny(string(out), "File exists", "RTNETLINK answers: File exists") {
				continue
			}
			return fmt.Errorf("ip %v 失败: %v (%s)", args, err, trimOut(string(out)))
		}
	}
	return nil
}

// ConfigureNoAddr 只设置 MTU 并启用设备，不分配 IPv4 地址。
//
// 为什么刻意不给隧道配地址：
// 实测发现 ZeroTier 会绑定到本机所有带地址的接口，并把 slx0 的地址
// 当作自己对外发包的源地址 —— ZT 的 P2P 流量因此在隧道里来回跑，
// 把隧道带宽吃掉，还会形成难以排查的环路。
// 不给地址，ZT 就不会认领它；内层路由改用显式 `ip route add` 建立。
func (d *Device) ConfigureNoAddr() error {
	if _, err := exec.LookPath("ip"); err != nil {
		return fmt.Errorf("找不到 ip 命令，无法配置网卡\n" +
			"提示: OpenWrt 执行 opkg install ip-full；CentOS 执行 yum install iproute")
	}
	for _, args := range [][]string{
		{"link", "set", "dev", d.name, "mtu", itoa(d.mtu)},
		{"link", "set", "dev", d.name, "up"},
	} {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("ip %v 失败: %v (%s)", args, err, trimOut(string(out)))
		}
	}
	return nil
}

// SetNonblock 把设备置为非阻塞，供需要 epoll 的场景使用。
func (d *Device) SetNonblock(on bool) error {
	flags, err := unix.FcntlInt(d.f.f.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if on {
		flags |= unix.O_NONBLOCK
	} else {
		flags &^= unix.O_NONBLOCK
	}
	_, err = unix.FcntlInt(d.f.f.Fd(), unix.F_SETFL, flags)
	return err
}

// IsNonblockError 判断错误是否为「非阻塞模式下暂无数据」。
func IsNonblockError(err error) bool {
	return err == syscall.EAGAIN || err == syscall.EWOULDBLOCK
}

func itoa(v int) string { return fmt.Sprintf("%d", v) }

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func trimOut(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
