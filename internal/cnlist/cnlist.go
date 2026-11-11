// Package cnlist 负责获取与缓存「中国大陆 IP 段」列表。
//
// 这是分流的基础数据：目标地址落在这个列表里就走原路，否则走隧道。
//
// 设计要点（DESIGN.md §7.3）：
//   - 不内置写死的数据（那会随时间过期且无法更新）；
//   - 优先用本地缓存，过期才联网；
//   - 多个源依次尝试，全失败时明确告警而不是静默降级；
//   - 缓存写入用原子替换，避免写一半掉电留下坏文件。
package cnlist

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Format 是数据源的格式。
type Format string

const (
	// FormatCIDR 每行一个 CIDR，如 "1.1.8.0/24"。
	FormatCIDR Format = "cidr"
	// FormatAPNIC 是 APNIC 官方 delegated 文件，形如
	// "apnic|CN|ipv4|1.0.1.0|256|20110414|allocated"。
	FormatAPNIC Format = "apnic"
)

// Source 是一个数据源。
type Source struct {
	Name   string
	URL    string
	Format Format
}

// DefaultSources 是默认源列表，按优先级排列。
//
// 刻意把国内可达的源放前面：实测 GitHub raw / jsdelivr 在国内的家宽上
// 全部不可达（超时 000），而 clang.cn 与 APNIC 都可直连。
// 这也符合「分流数据必须能在境内更新」这个硬约束。
var DefaultSources = []Source{
	{
		Name:   "ispip.clang.cn",
		URL:    "https://ispip.clang.cn/all_cn_cidr.txt",
		Format: FormatCIDR,
	},
	{
		Name:   "apnic",
		URL:    "https://ftp.apnic.net/apnic/stats/apnic/delegated-apnic-latest",
		Format: FormatAPNIC,
	},
}

// DefaultCachePath 是缓存文件路径。
//
// 放在 /etc/slx 而不是 /tmp：分区表（tmpfs）重启就没了，
// 而分流数据是「没它就完全不能工作」的硬依赖，必须跨重启保留。
// 单次写入约 60KB，一天最多写一次，对 eMMC 的磨损可以忽略。
const DefaultCachePath = "/etc/slx/cn.cache"

// CacheMaxAge 是缓存的有效期。
//
// 24 小时是个折中：国内 IP 段每天都有少量变化，但没必要更频繁地
// 依赖网络；超过这个时长才联网更新。
const CacheMaxAge = 24 * time.Hour

// Result 是获取结果，含来源信息便于日志与诊断。
type Result struct {
	CIDRs     []string
	FromCache bool
	Source    string
	CacheAge  time.Duration
	Updated   time.Time
	// Offline 表示这份网段表来自离线兜底（ip2region 静态库），不是在线源。
	// 值班的人看到这一位要知道：数据比在线源旧。
	Offline bool
}

// Load 获取中国大陆 IP 段列表。
//
// 顺序：新鲜缓存 → 依次尝试各在线源 → 过期缓存（并告警）。
// 三者都失败时返回错误 —— 此时绝不能启用分流，
// 因为「分不清境内境外」比「不分流」危险得多。
func Load(cachePath string, sources []Source, allowExpiredCache bool) (*Result, error) {
	if cachePath == "" {
		cachePath = DefaultCachePath
	}
	if len(sources) == 0 {
		sources = DefaultSources
	}

	// —— 1. 新鲜缓存 ——
	if r, err := readCache(cachePath); err == nil && r != nil {
		age := time.Since(r.updated)
		if age <= CacheMaxAge {
			return &Result{CIDRs: r.cidrs, FromCache: true, Source: r.source,
				CacheAge: age, Updated: r.updated}, nil
		}
	}

	// —— 2. 在线源 ——
	var lastErr error
	for _, src := range sources {
		cidrs, err := fetch(context.Background(), src, 30*time.Second)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", src.Name, err)
			continue
		}
		if len(cidrs) < 100 {
			lastErr = fmt.Errorf("%s: 返回的条目太少（%d 条），疑似源异常", src.Name, len(cidrs))
			continue
		}
		now := time.Now()
		_ = writeCache(cachePath, cidrs, src.Name, now)
		return &Result{CIDRs: cidrs, Source: src.Name, Updated: now}, nil
	}

	// —— 3. 过期缓存兜底 ——
	if allowExpiredCache {
		if r, err := readCache(cachePath); err == nil && r != nil {
			age := time.Since(r.updated)
			return &Result{CIDRs: r.cidrs, FromCache: true, Source: r.source,
				CacheAge: age, Updated: r.updated}, nil
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的数据源")
	}
	return nil, fmt.Errorf("获取中国大陆 IP 段失败: %w\n"+
		"提示: 分流依赖这份数据，取不到时不能启用分流（否则会把境内流量也送进隧道）\n"+
		"可从其它机器拷贝缓存文件到 %s", lastErr, cachePath)
}

// LoadCached 只读本地缓存，不联网。
//
// 用途：交叉校验要对着「当前设备上实际在用的那份」查，
// 而不是现拉一份新的 —— 那会掩盖正在发生的问题。
func LoadCached(cachePath string) (*Result, error) {
	if cachePath == "" {
		cachePath = DefaultCachePath
	}
	r, err := readCache(cachePath)
	if err != nil {
		return nil, fmt.Errorf("读取国内网段缓存 %s: %w", cachePath, err)
	}
	return &Result{CIDRs: r.cidrs, FromCache: true, Source: r.source,
		CacheAge: time.Since(r.updated), Updated: r.updated}, nil
}

// fetch 从一个源下载并解析。
func fetch(ctx context.Context, src Source, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, err
	}
	// 有的源会按 UA 拒绝默认客户端
	req.Header.Set("User-Agent", "slx/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	switch src.Format {
	case FormatAPNIC:
		return parseAPNIC(resp.Body)
	default:
		return parseCIDRLines(resp.Body)
	}
}

// parseCIDRLines 解析「每行一个 CIDR」格式。
func parseCIDRLines(r io.Reader) ([]string, error) {
	var out []string
	seen := make(map[string]struct{}, 8192)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		// 只取第一个字段，容忍行尾有注释
		if i := strings.IndexAny(line, " \t#"); i > 0 {
			line = line[:i]
		}
		cidr, ok := normalizeCIDR(line)
		if !ok {
			continue
		}
		if _, dup := seen[cidr]; dup {
			continue
		}
		seen[cidr] = struct{}{}
		out = append(out, cidr)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// parseAPNIC 解析 APNIC delegated 文件里的 CN ipv4 记录。
//
// 格式：registry|cc|type|start|value|date|status
// 对 ipv4，value 是地址数量（不是掩码位），需要换算成 CIDR。
func parseAPNIC(r io.Reader) ([]string, error) {
	var out []string
	seen := make(map[string]struct{}, 16384)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "apnic|") {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) < 5 {
			continue
		}
		if f[1] != "CN" || f[2] != "ipv4" {
			continue
		}
		// 只取已分配（allocated）与已指派（assigned）
		if len(f) >= 7 && f[6] != "allocated" && f[6] != "assigned" {
			continue
		}
		start := net.ParseIP(f[3]).To4()
		if start == nil {
			continue
		}
		count, err := strconv.Atoi(f[4])
		if err != nil || count <= 0 {
			continue
		}
		// 数量必须是 2 的幂才能表示成单个 CIDR；不是的话按实际大小展开
		prefix := maskForCount(count)
		if prefix < 0 {
			continue
		}
		cidr := fmt.Sprintf("%s/%d", start.String(), prefix)
		if _, dup := seen[cidr]; dup {
			continue
		}
		seen[cidr] = struct{}{}
		out = append(out, cidr)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// maskForCount 把「地址数量」换算成 CIDR 前缀长度。
func maskForCount(count int) int {
	for bits := 0; bits <= 32; bits++ {
		if count == 1<<uint(32-bits) {
			return bits
		}
	}
	return -1
}

// normalizeCIDR 校验并规范化一个 CIDR 字符串。
func normalizeCIDR(s string) (string, bool) {
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return "", false
	}
	v4 := ip.To4()
	if v4 == nil {
		return "", false // 只处理 IPv4
	}
	// 用网络地址规范化，避免 1.1.8.5/24 这类非网络号写法进 ipset 报错
	return ipnet.String(), true
}

// —— 缓存 ——

type cacheData struct {
	cidrs   []string
	source  string
	updated time.Time
}

// readCache 读取缓存。首行是元信息，其余是 CIDR。
func readCache(path string) (*cacheData, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	if !sc.Scan() {
		return nil, fmt.Errorf("缓存文件为空")
	}
	d := &cacheData{}
	// 首行格式： #slx-cn 1683000000 ispip.clang.cn
	head := strings.Fields(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "#slx-cn"))
	if len(head) >= 2 {
		if ts, err := strconv.ParseInt(head[0], 10, 64); err == nil {
			d.updated = time.Unix(ts, 0)
		}
		d.source = head[1]
	}
	if d.updated.IsZero() {
		return nil, fmt.Errorf("缓存缺少时间戳")
	}

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		d.cidrs = append(d.cidrs, line)
	}
	if len(d.cidrs) == 0 {
		return nil, fmt.Errorf("缓存没有条目")
	}
	return d, sc.Err()
}

// writeCache 原子写入缓存。
func writeCache(path string, cidrs []string, source string, updated time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("#slx-cn %d %s\n", updated.Unix(), source))
	for _, c := range cidrs {
		b.WriteString(c)
		b.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Summary 生成一行摘要，用于启动日志。
func (r *Result) Summary() string {
	age := time.Since(r.Updated)
	how := "在线获取"
	if r.FromCache {
		how = "本地缓存"
	}
	return fmt.Sprintf("%d 条，来源 %s（%s，%s前更新）",
		len(r.CIDRs), r.Source, how, humanDur(age))
}

func humanDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "不到 1 分钟"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天", int(d.Hours()/24))
	}
}
