// Package ipgeo 用 ip2region 的离线 xdb 库查询 IP 归属地。
//
// 为什么需要它（DESIGN.md §7.1）：
//
//	分流目前只有「在不在国内段」这一个比特 —— cnlist 的 CIDR 列表灌进 ipset，
//	内核按段表判定境内直连 / 境外走隧道。这个比特在「为什么这个域名走了隧道」
//	这类问题上什么也答不出来：它只知道在不在，不知道在哪。
//
//	ip2region 的 xdb 给出国家/省/市/运营商，把这个比特补成一个可读的归属地。
//	诊断（slx ip / doctor / trace）与后续的精细策略都用得上。
//
// 设计约束：
//
//   - xdb 是独立文件，不进二进制。slx 要出 5 个架构的静态包，体积敏感
//     （DESIGN.md 里传 7MB 二进制都费劲），11MB 的库更不能打进去；
//   - 文件缺失是「少一项能力」，不是故障 —— 由调用方决定如何降级；
//   - 查不到返回空结果而不是报错（ip2region 自己的约定）。
package ipgeo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

// DefaultXDBPath 是 IPv4 xdb 的默认位置。
//
// 与 cnlist 的缓存放同一个目录：/etc/slx 下已经是「运行时数据」的归属地，
// 这两个文件都是「没有它就少一项能力」而不是「没有它就不能启动」。
const DefaultXDBPath = "/etc/slx/ip2region_v4.xdb"

// DefaultXDBPathV6 是 IPv6 xdb 的默认位置。
const DefaultXDBPathV6 = "/etc/slx/ip2region_v6.xdb"

// PathEnv 是用环境变量指定 xdb 路径的变量名。
const PathEnv = "SLX_IP2REGION_XDB"

// ErrNoDatabase 表示本机没有对应的 xdb 文件。
var ErrNoDatabase = errors.New("ip2region xdb 数据库不存在")

// Region 是一次归属地查询的结果。
type Region struct {
	Country  string // 国家或地区，如 中国 / United States
	Province string // 省级，如 广东省；境外常为空
	City     string // 市级，如 深圳市
	ISP      string // 运营商，如 电信；未知时为空
	ISO      string // 两位国家码，如 CN
}

// IsCN 判断是否落在中国境内。
func (r Region) IsCN() bool {
	return r.ISO == "CN" || r.Country == "中国"
}

// Empty 判断查询是否没有结果。
func (r Region) Empty() bool {
	return r.Country == "" && r.Province == "" && r.City == "" && r.ISP == "" && r.ISO == ""
}

// String 返回一行可读描述。
func (r Region) String() string {
	parts := make([]string, 0, 4)
	for _, s := range []string{r.Country, r.Province, r.City, r.ISP} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "未知"
	}
	out := strings.Join(parts, " ")
	if r.ISO != "" {
		out += " (" + r.ISO + ")"
	}
	return out
}

// Lookup 是一个 xdb 查询器。
//
// 底层 ip2region 的 Searcher 明确不是并发安全的，所以每个并发使用者
// 各自建一个 Lookup，不要跨 goroutine 共享。
type Lookup struct {
	v4     *xdb.Searcher
	v6     *xdb.Searcher
	v4Path string
	v6Path string
}

// Open 打开一个 xdb 查询器。
//
// 缓存策略固定用 vectorIndex：512KB 常驻，换掉一次磁盘 IO，
// 实测单次查询在百微秒量级。全量入内存虽然更快，但 11MB 常驻
// 对路由器不友好（DESIGN.md 里对内存有明确体检阈值）。
//
// 同目录如果有 ip2region_v6.xdb，会自动一并挂上 —— IPv6 归属地是加分项，
// 它缺席不该影响 IPv4，所以这里失败只忽略、不报错。
func Open(v4Path string) (*Lookup, error) {
	if v4Path == "" {
		v4Path = DefaultXDBPath
	}
	if err := VerifyFile(v4Path); err != nil {
		return nil, err
	}
	vIndex, err := xdb.LoadVectorIndexFromFile(v4Path)
	if err != nil {
		return nil, fmt.Errorf("加载 xdb 索引 %s: %w", v4Path, err)
	}
	searcher, err := xdb.NewWithVectorIndex(xdb.IPv4, v4Path, vIndex)
	if err != nil {
		return nil, fmt.Errorf("创建 xdb 查询器: %w", err)
	}
	l := &Lookup{v4: searcher, v4Path: v4Path}
	if v6Path := siblingV6Path(v4Path); v6Path != "" {
		if s, err := openV6(v6Path); err == nil {
			l.v6 = s
			l.v6Path = v6Path
		}
	}
	return l, nil
}

// openV6 打开一个 IPv6 xdb 查询器。
func openV6(v6Path string) (*xdb.Searcher, error) {
	if err := xdb.VerifyFromFile(v6Path); err != nil {
		return nil, fmt.Errorf("xdb 文件 %s 校验失败: %w", v6Path, err)
	}
	vIndex, err := xdb.LoadVectorIndexFromFile(v6Path)
	if err != nil {
		return nil, fmt.Errorf("加载 xdb 索引 %s: %w", v6Path, err)
	}
	searcher, err := xdb.NewWithVectorIndex(xdb.IPv6, v6Path, vIndex)
	if err != nil {
		return nil, fmt.Errorf("创建 xdb 查询器: %w", err)
	}
	return searcher, nil
}

// siblingV6Path 猜同目录下的 v6 xdb 路径。
//
// ip2region_v4.xdb → ip2region_v6.xdb。
// 猜不到就返回空串（不启用 IPv6）。
func siblingV6Path(v4Path string) string {
	dir := filepath.Dir(v4Path)
	name := filepath.Base(v4Path)
	if !strings.Contains(name, "_v4.") {
		return ""
	}
	candidate := filepath.Join(dir, strings.Replace(name, "_v4.", "_v6.", 1))
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}
	return candidate
}

// OpenWithV6 打开同时支持 IPv4 与 IPv6 的查询器。
//
// v6Path 为空或文件不存在时只启用 IPv4 —— 这不算错误：
// IPv6 归属地是加分项，不该因为它缺席就让 IPv4 也用不了。
func OpenWithV6(v4Path, v6Path string) (*Lookup, error) {
	l, err := Open(v4Path)
	if err != nil {
		return nil, err
	}
	if v6Path == "" {
		return l, nil
	}
	if _, statErr := os.Stat(v6Path); statErr != nil {
		if os.IsNotExist(statErr) {
			return l, nil
		}
		l.Close()
		return nil, fmt.Errorf("访问 xdb 文件 %s: %w", v6Path, statErr)
	}
	searcher, err := openV6(v6Path)
	if err != nil {
		l.Close()
		return nil, err
	}
	l.v6 = searcher
	l.v6Path = v6Path
	return l, nil
}

// VerifyFile 校验 xdb 文件能否被当前查询器使用。
//
// 为什么要校验：xdb 格式改过好几版（v2 / v3），文件与查询器版本不匹配
// 会静默查出错误结果，而不是报错。启动时校验一次，别等线上查错。
func VerifyFile(path string) error {
	if path == "" {
		return fmt.Errorf("%w: 空路径", ErrNoDatabase)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNoDatabase, path)
		}
		return fmt.Errorf("访问 xdb 文件 %s: %w", path, err)
	}
	if err := xdb.VerifyFromFile(path); err != nil {
		return fmt.Errorf("xdb 文件 %s 校验失败: %w", path, err)
	}
	return nil
}

// Lookup 查询一个 IP 的归属地。
//
// 查不到时返回空的 Region 和 nil error —— 「这个 IP 不在库里」是正常结果。
func (l *Lookup) Lookup(ip string) (Region, error) {
	if l == nil {
		return Region{}, ErrNoDatabase
	}
	parsed, err := xdb.ParseIP(ip)
	if err != nil {
		return Region{}, fmt.Errorf("解析 IP %q: %w", ip, err)
	}
	var searcher *xdb.Searcher
	if len(parsed) == 4 {
		searcher = l.v4
	} else {
		searcher = l.v6
	}
	if searcher == nil {
		return Region{}, fmt.Errorf("没有可用的 %s 查询器", ipVersionName(len(parsed)))
	}
	raw, err := searcher.Search(parsed)
	if err != nil {
		return Region{}, fmt.Errorf("查询 %s: %w", ip, err)
	}
	return parseRegion(raw), nil
}

// IsCN 判断 IP 是否落在中国境内。
//
// 注意：这是「按归属地库判定」，与分流实际使用的 ipset 段表是两套独立数据。
// 两者对同一个 IP 给出不同结论，说明至少有一方过期了 —— 这正是交叉校验的价值。
func (l *Lookup) IsCN(ip string) bool {
	r, err := l.Lookup(ip)
	if err != nil {
		return false
	}
	return r.IsCN()
}

// Path 返回实际使用的 IPv4 xdb 文件路径。
func (l *Lookup) Path() string {
	if l == nil {
		return ""
	}
	return l.v4Path
}

// Close 释放文件句柄。
func (l *Lookup) Close() {
	if l == nil {
		return
	}
	if l.v4 != nil {
		l.v4.Close()
		l.v4 = nil
	}
	if l.v6 != nil {
		l.v6.Close()
		l.v6 = nil
	}
}

// ResolvePath 按「显式参数 > 环境变量 > 默认路径」的顺序决定 xdb 文件位置。
func ResolvePath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := os.Getenv(PathEnv); v != "" {
		return v
	}
	return DefaultXDBPath
}

// parseRegion 把 ip2region 的字符串解析成结构体。
//
// xdb v3 的格式是 Country|Province|City|ISP|ISO，例如
// 「中国|广东省|深圳市|电信|CN」「Australia|Queensland|Brisbane|0|AU」。
// 历史上还有不带 ISO 的 4 段格式，一并兼容。
//
// 各段里的 "0" 是 ip2region 的「未知」占位（例如 8.8.8.8 的城市位），
// 统一转成空串，别把 "0" 显示给用户。
func parseRegion(raw string) Region {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Region{}
	}
	f := strings.Split(raw, "|")
	get := func(i int) string {
		if i >= len(f) {
			return ""
		}
		v := strings.TrimSpace(f[i])
		if v == "0" {
			return ""
		}
		return v
	}
	r := Region{
		Country:  get(0),
		Province: get(1),
		City:     get(2),
		ISP:      get(3),
	}
	if len(f) >= 5 {
		r.ISO = get(4)
	}
	return r
}

func ipVersionName(n int) string {
	if n == 4 {
		return "IPv4"
	}
	return "IPv6"
}
