package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 日志命令默认值。
const (
	logsDefaultTail = 50
	logsMaxTail     = 5000
)

// defaultLogPath 是守护进程日志的默认落盘位置。
//
// 放 var/log 而不是 tmp：日志要跨重启保留，才能事后排查
// 「昨天半夜断了一下」这类问题。OpenWrt 上 /var 是 tmpfs，
// 所以那里会退回到配置里的 log_file。
func defaultLogPath() string {
	if p := os.Getenv("SLX_LOG_FILE"); p != "" {
		return p
	}
	return "/var/log/slx.log"
}

// Logs 实现 `slx logs`。
//
// 支持：
//
//	slx logs                         看最后 50 行
//	slx logs -n 200                  看最后 200 行
//	slx logs --level ERROR           只看错误
//	slx logs --event dispatch        只看某个事件族（前缀匹配）
//	slx logs --layer 分流            只看某一层
func Logs(args []string) int {
	path := defaultLogPath()
	tail := logsDefaultTail
	var level, event, layer string

	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch a {
		case "-n", "--tail", "--lines":
			if v := next(); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					tail = n
				}
			}
		case "--level", "-l":
			level = strings.ToUpper(next())
		case "--event", "-e":
			event = next()
		case "--layer":
			layer = next()
		case "--file", "-f":
			if v := next(); v != "" {
				path = v
			}
		case "-h", "--help":
			fmt.Println(`用法：slx logs [选项]

选项：
  -n, --tail N      显示最后 N 行（默认 50，上限 5000）
  -l, --level LVL   只看某个级别：DEBUG / INFO / WARN / ERROR
  -e, --event PREFIX 只看某个事件族，如 dispatch、netcfg、forward
      --layer NAME  只看某一层：分流 / 转发 / 选路 / 链路 / DNS / 组网
  -f, --file PATH   指定日志文件（默认 /var/log/slx.log）`)
			return 0
		}
	}

	if tail > logsMaxTail {
		tail = logsMaxTail
	}

	lines, err := tailLines(path, tail, func(line string) bool {
		return matchLogLine(line, level, event, layer)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取日志失败：%v\n", err)
		fmt.Println()
		fmt.Println("可能的原因：")
		fmt.Printf("  1. 日志没写到文件（当前找的是 %s）\n", path)
		fmt.Println("  2. 守护进程是通过 systemd/procd 启动的，日志进了 journald/syslog")
		fmt.Println()
		fmt.Println("如果是第 2 种情况，请用系统自带的方式看：")
		fmt.Println("  systemd：  journalctl -u slx -n 100 --no-pager")
		fmt.Println("  OpenWrt：  logread | grep slx")
		fmt.Println()
		fmt.Println("也可以让 slx 自己落盘：在配置里加 log_file: /var/log/slx.log")
		return 1
	}

	if len(lines) == 0 {
		fmt.Println("没有匹配的日志。")
		return 0
	}

	for _, l := range lines {
		fmt.Println(renderLogLine(l))
	}
	return 0
}

// matchLogLine 判断一行日志是否满足过滤条件。
func matchLogLine(line, level, event, layer string) bool {
	if !strings.HasPrefix(strings.TrimSpace(line), "{") {
		// 非结构化行（启动横幅之类）只在没有过滤条件时显示
		return level == "" && event == "" && layer == ""
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return false
	}
	if level != "" {
		if lv, _ := rec["level"].(string); !strings.EqualFold(lv, level) {
			return false
		}
	}
	if event != "" {
		ev, _ := rec["event"].(string)
		if !strings.HasPrefix(ev, event) {
			return false
		}
	}
	if layer != "" {
		ly, _ := rec["layer"].(string)
		if ly != layer {
			return false
		}
	}
	return true
}

// renderLogLine 把结构化日志渲染成人读的一行。
func renderLogLine(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") {
		return line
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return line
	}

	var b strings.Builder
	if t, _ := rec["time"].(string); t != "" {
		// 只保留 时:分:秒
		if len(t) >= 19 {
			b.WriteString(t[11:19])
			b.WriteString("  ")
		}
	}
	if lv, _ := rec["level"].(string); lv != "" {
		b.WriteString(fmt.Sprintf("%-5s ", lv))
	}
	if msg, _ := rec["msg"].(string); msg != "" {
		b.WriteString(msg)
	}
	if ev, _ := rec["event"].(string); ev != "" {
		b.WriteString("  [" + ev + "]")
	}

	// 其余字段挑几个关键的显示，避免一行过长
	for _, k := range []string{"err", "hint", "node_id", "dst", "dev", "ifaces", "addr",
		"egress", "inner", "tun_read", "sent", "tun_write", "no_route", "send_err"} {
		if v, ok := rec[k]; ok {
			b.WriteString(fmt.Sprintf("  %s=%v", k, v))
		}
	}
	return b.String()
}

// tailLines 读取文件末尾若干行，按 keep 过滤后返回。
//
// 为了在嵌入式设备上也不吃内存，只保留一个大小为 tail 的环形缓冲，
// 不管文件多大都只占这么多。
func tailLines(path string, tail int, keep func(string) bool) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// 简单起见顺序读（日志文件不大，且嵌入式设备上 seek 反向解析更麻烦）。
	// 若文件超过 32MB 则从末尾 32MB 处开始读，避免把整个文件读进内存。
	const maxScan = 32 << 20
	if st, serr := f.Stat(); serr == nil && st.Size() > maxScan {
		if _, serr := f.Seek(st.Size()-maxScan, 0); serr == nil {
			// 丢弃第一行（可能是半行，避免渲染出残缺 JSON）
			br := bufio.NewReader(f)
			_, _ = br.ReadString('\n')
		}
	}

	ring := make([]string, 0, tail)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if keep != nil && !keep(line) {
			continue
		}
		if len(ring) < tail {
			ring = append(ring, line)
			continue
		}
		copy(ring, ring[1:])
		ring[len(ring)-1] = line
	}
	return ring, sc.Err()
}

// ensureLogDir 确保日志目录存在（供 run 启动时调用）。
func ensureLogDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
