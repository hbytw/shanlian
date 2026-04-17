package forward

import (
	"sync"
	"time"
)

// Manager 转发管理器
type Manager struct {
	rules   map[string]*ForwardRule
	clients map[string]*Client
	mu      sync.RWMutex
}

// Client HTTP 转发客户端
type Client struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	APIKey    string    `json:"api_key"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	LastSend  time.Time `json:"last_send"`
	FailCount int       `json:"fail_count"`
}

// ForwardRule 转发规则
type ForwardRule struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	NetworkID string    `json:"network_id"`
	Filter    string    `json:"filter"` // level, category, keyword
	TargetURL string    `json:"target_url"`
	APIKey    string    `json:"api_key"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

// NewManager 创建转发管理器
func NewManager(_ interface{}) *Manager {
	return &Manager{
		rules:   make(map[string]*ForwardRule),
		clients: make(map[string]*Client),
	}
}

// Create 创建转发规则
func (m *Manager) Create(rule *ForwardRule) error {
	if rule.ID == "" {
		rule.ID = generateID("rule")
	}
	rule.CreatedAt = time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.rules[rule.ID] = rule
	return nil
}

// List 列出所有转发规则
func (m *Manager) List() ([]*ForwardRule, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rules := make([]*ForwardRule, 0, len(m.rules))
	for _, rule := range m.rules {
		rules = append(rules, rule)
	}
	return rules, nil
}

// Match 检查日志是否匹配规则
func (m *Manager) Match(rule *ForwardRule, level, category, message string) bool {
	if !rule.Active {
		return false
	}

	switch rule.Filter {
	case "error":
		return level == "error"
	case "warn":
		return level == "warn" || level == "error"
	case "all":
		return true
	default:
		return true
	}
}

func generateID(prefix string) string {
	return prefix + "_" + time.Now().Format("20060102150405")
}
