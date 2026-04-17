package alert

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/shanlian/audit/internal/storage"
)

// 告警规则
type Rule struct {
	Name       string
	Level      string // info, warn, error
	Condition  func(*LogEntry) bool
	Message    string
	Throttle   time.Duration // 同一规则的最小告警间隔
	lastAlert  time.Time
	lastAlertMu sync.Mutex
}

// LogEntry 日志条目
type LogEntry struct {
	NetworkID string
	PeerID    string
	Level     string
	Category  string
	Message   string
	Metadata  map[string]interface{}
}

// Engine 告警引擎
type Engine struct {
	store *storage.SQLiteStorage
	rules []Rule
	mu    sync.RWMutex
}

// NewEngine 创建告警引擎
func NewEngine(store *storage.SQLiteStorage) *Engine {
	engine := &Engine{
		store: store,
		rules: defaultRules(),
	}
	return engine
}

// defaultRules 返回默认告警规则
func defaultRules() []Rule {
	return []Rule{
		{
			Name:   "error_rate_high",
			Level:  "error",
			Condition: func(e *LogEntry) bool {
				return e.Level == "error" && strings.Contains(e.Message, "connection failed")
			},
			Message:  "错误率过高：连接失败",
			Throttle: 5 * time.Minute,
		},
		{
			Name:   "auth_failure",
			Level:  "warn",
			Condition: func(e *LogEntry) bool {
				return e.Category == "auth" && strings.Contains(e.Message, "failed")
			},
			Message:  "认证失败",
			Throttle: 2 * time.Minute,
		},
		{
			Name:   "traffic_quota",
			Level:  "info",
			Condition: func(e *LogEntry) bool {
				if quota, ok := e.Metadata["quota_percent"].(float64); ok {
					return quota > 80
				}
				return false
			},
			Message:  "流量配额使用超过 80%",
			Throttle: 1 * time.Hour,
		},
		{
			Name:   "moon_offline",
			Level:  "error",
			Condition: func(e *LogEntry) bool {
				return e.Category == "moon" && strings.Contains(e.Message, "offline")
			},
			Message:  "Moon 节点离线",
			Throttle: 1 * time.Minute,
		},
	}
}

// CheckLog 检查日志并触发告警
func (e *Engine) CheckLog(req *storage.LogRequest) {
	entry := &LogEntry{
		NetworkID: req.NetworkID,
		PeerID:    req.PeerID,
		Level:     req.Level,
		Category:  req.Category,
		Message:   req.Message,
	}

	// 解析 metadata
	if req.Metadata != "" {
		if m, err := storage.ParseMetadata(req.Metadata); err == nil {
			entry.Metadata = m
		}
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, rule := range e.rules {
		if rule.Condition(entry) {
			// 检查 throttle
			rule.lastAlertMu.Lock()
			if time.Since(rule.lastAlert) < rule.Throttle {
				rule.lastAlertMu.Unlock()
				continue
			}
			rule.lastAlert = time.Now()
			rule.lastAlertMu.Unlock()

			// 发送告警
			details := ""
			if data, err := storage.ParseMetadata(req.Metadata); err == nil {
				if bytes, err := json.Marshal(data); err == nil {
					details = string(bytes)
				}
			}

			if err := e.store.InsertAlert(
				req.NetworkID,
				req.PeerID,
				rule.Level,
				rule.Name,
				rule.Message,
				details,
			); err != nil {
				log.Printf("插入告警失败: %v", err)
			} else {
				log.Printf("触发告警: [%s] %s - %s", rule.Level, rule.Name, rule.Message)
			}
		}
	}
}

// QueryAlerts 查询告警
func (e *Engine) QueryAlerts(filter *storage.AlertFilter) ([]*storage.Alert, int64, error) {
	return e.store.QueryAlerts(filter)
}

// StartAlertChecker 启动告警检查器
func (e *Engine) StartAlertChecker() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			<-ticker.C
			e.checkSystemHealth()
		}
	}()
}

// checkSystemHealth 检查系统健康状态
func (e *Engine) checkSystemHealth() {
	// 查询最近 5 分钟的错误日志
	logs, _, err := e.store.QueryLogs(&storage.LogFilter{
		Level:     "error",
		StartTime: time.Now().Add(-5 * time.Minute).Format("2006-01-02 15:04:05"),
		Limit:     100,
	})
	if err != nil {
		log.Printf("健康检查查询失败: %v", err)
		return
	}

	if len(logs) > 50 {
		details, _ := json.Marshal(map[string]interface{}{"count": len(logs)})
		if err := e.store.InsertAlert("", "", "error", "error_flood",
			"错误日志过多", string(details)); err != nil {
			log.Printf("插入告警失败: %v", err)
		}
	}
}
