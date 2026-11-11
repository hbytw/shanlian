// Package logx 提供结构化日志。
//
// 设计要点（DESIGN.md §8.5）：
//   - 默认输出 JSON 行，便于机器聚合；也支持 text 便于人读；
//   - 统一字段名，否则无法聚合分析；
//   - 关键状态变化必须带 hint（可执行的修复建议），而不只是说"失败了"；
//   - 每条与选路相关的日志带 decision_id，可串起「探测→评分→切换→结果」的完整因果。
package logx

import (
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"time"
)

// 统一字段名。集中定义，避免各模块拼写不一致导致日志无法聚合。
const (
	FieldEvent      = "event"       // 事件名，稳定标识
	FieldHint       = "hint"        // 可执行建议
	FieldDecisionID = "decision_id" // 决策链路 ID
	FieldNodeID     = "node_id"
	FieldEgress     = "egress"
	FieldTransport  = "transport"
	FieldLayer      = "layer"  // 故障所属层：分流/组网/选路/链路/DNS/转发
	FieldTarget     = "target" // 目的地（域名或 IP）
	FieldErr        = "err"
)

// Options 控制日志输出。
type Options struct {
	Level  string    // debug | info | warn | error（默认 info）
	Format string    // json | text（默认 json）
	Writer io.Writer // 默认 stderr
}

// ParseLevel 把字符串解析为 slog 级别，未知值回落到 info。
func ParseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New 按选项构造日志器。
func New(opts Options) *slog.Logger {
	w := opts.Writer
	if w == nil {
		w = os.Stderr
	}
	h := &slog.HandlerOptions{Level: ParseLevel(opts.Level)}
	var handler slog.Handler
	if opts.Format == "text" {
		handler = slog.NewTextHandler(w, h)
	} else {
		handler = slog.NewJSONHandler(w, h)
	}
	return slog.New(handler)
}

// Discard 返回一个丢弃全部日志的日志器（测试用）。
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// Event 构造事件名字段。事件名应当是稳定的短标识，例如 "egress.switch"。
func Event(name string) slog.Attr { return slog.String(FieldEvent, name) }

// Hint 构造修复建议字段。用于「失败」类日志，告诉用户下一步该做什么。
func Hint(text string) slog.Attr { return slog.String(FieldHint, text) }

// DecisionID 构造决策链路 ID 字段。
func DecisionID(id string) slog.Attr { return slog.String(FieldDecisionID, id) }

// Layer 构造故障层次字段，取值见 LayerXxx。
func Layer(l string) slog.Attr { return slog.String(FieldLayer, l) }

// 故障层次。故障自动分类时用它标明问题出在哪一层。
const (
	LayerDispatch = "分流" // ipset / fwmark / 策略路由
	LayerMesh     = "组网" // ZeroTier / 邻居发现
	LayerSelect   = "选路" // 出口选择
	LayerLink     = "链路" // RTT / 丢包 / 吞吐
	LayerDNS      = "dns"
	LayerForward  = "转发" // NAT / TUN
)

var decisionSeq atomic.Uint64

// NewDecisionID 生成一个决策链路 ID。
//
// 格式：单调递增序号 + 时间戳低位，够用且无需全局协调。
func NewDecisionID() string {
	n := decisionSeq.Add(1)
	return itoa(n) + "-" + itoa(uint64(time.Now().UnixNano()>>20)&0xfffff)
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
