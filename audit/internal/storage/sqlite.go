package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// LogRequest 日志请求
type LogRequest struct {
	ID           int64     `json:"id"`
	APIKey       string    `json:"api_key"`
	NetworkID    string    `json:"network_id"`
	PeerID       string    `json:"peer_id"`
	Level        string    `json:"level"` // info, warn, error
	Category     string    `json:"category"`
	Message      string    `json:"message"`
	Metadata     string    `json:"metadata"` // JSON 格式的额外数据
	IPAddress    string    `json:"ip_address"`
	UserAgent    string    `json:"user_agent"`
	ReceivedAt   time.Time `json:"received_at"`
}

// LogFilter 日志查询过滤器
type LogFilter struct {
	NetworkID string
	PeerID    string
	Level     string
	StartTime string
	EndTime   string
	Limit     int
	Offset    int
}

// TrafficStats 流量统计
type TrafficStats struct {
	NetworkID   string  `json:"network_id"`
	Period      string  `json:"period"`
	BytesSent   int64   `json:"bytes_sent"`
	BytesRecv   int64   `json:"bytes_recv"`
	PacketCount int64   `json:"packet_count"`
	RecordCount int64   `json:"record_count"`
	StartTime   string  `json:"start_time"`
	EndTime     string  `json:"end_time"`
}

// SQLiteStorage SQLite 存储
type SQLiteStorage struct {
	db       *sql.DB
	mu       sync.RWMutex
	stopCh   chan struct{}
	closed   bool
}

// NewSQLiteStorage 创建 SQLite 存储
func NewSQLiteStorage(dbPath string) (*SQLiteStorage, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=10000")
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	// 设置连接池
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

	store := &SQLiteStorage{
		db:     db,
		stopCh: make(chan struct{}),
	}

	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}

	return store, nil
}

// migrate 执行数据库迁移
func (s *SQLiteStorage) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		api_key TEXT NOT NULL,
		network_id TEXT,
		peer_id TEXT,
		level TEXT NOT NULL,
		category TEXT,
		message TEXT NOT NULL,
		metadata TEXT,
		ip_address TEXT,
		user_agent TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		received_at TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_logs_network_peer ON logs(network_id, peer_id);
	CREATE INDEX IF NOT EXISTS idx_logs_level ON logs(level);
	CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);

	CREATE TABLE IF NOT EXISTS traffic_aggregation (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		network_id TEXT NOT NULL,
		period TEXT NOT NULL,
		period_start TIMESTAMP NOT NULL,
		period_end TIMESTAMP NOT NULL,
		bytes_sent INTEGER DEFAULT 0,
		bytes_recv INTEGER DEFAULT 0,
		packet_count INTEGER DEFAULT 0,
		record_count INTEGER DEFAULT 0,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_traffic_agg_network_period ON traffic_aggregation(network_id, period_start);

	CREATE TABLE IF NOT EXISTS alerts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		network_id TEXT,
		peer_id TEXT,
		level TEXT NOT NULL,
		rule_name TEXT NOT NULL,
		message TEXT NOT NULL,
		details TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		acknowledged INTEGER DEFAULT 0,
		acknowledged_at TIMESTAMP,
		acknowledged_by TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_alerts_network ON alerts(network_id);
	CREATE INDEX IF NOT EXISTS idx_alerts_level ON alerts(level);
	CREATE INDEX IF NOT EXISTS idx_alerts_created_at ON alerts(created_at);
	`

	_, err := s.db.Exec(schema)
	return err
}

// Close 关闭存储
func (s *SQLiteStorage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	close(s.stopCh)
	return s.db.Close()
}

// Stop 停止后台任务
func (s *SQLiteStorage) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.stopCh)
	}
}

// InsertLog 插入日志
func (s *SQLiteStorage) InsertLog(req *LogRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	metadata := req.Metadata
	if metadata == "" {
		metadata = "{}"
	}

	result, err := s.db.Exec(`
		INSERT INTO logs (api_key, network_id, peer_id, level, category, message, metadata, ip_address, user_agent, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, req.APIKey, req.NetworkID, req.PeerID, req.Level, req.Category, req.Message, metadata, req.IPAddress, req.UserAgent, req.ReceivedAt)

	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()
	req.ID = id
	return nil
}

// QueryLogs 查询日志
func (s *SQLiteStorage) QueryLogs(filter *LogFilter) ([]*LogRequest, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	whereClause := "WHERE 1=1"
	args := []interface{}{}

	if filter.NetworkID != "" {
		whereClause += " AND network_id = ?"
		args = append(args, filter.NetworkID)
	}
	if filter.PeerID != "" {
		whereClause += " AND peer_id = ?"
		args = append(args, filter.PeerID)
	}
	if filter.Level != "" {
		whereClause += " AND level = ?"
		args = append(args, filter.Level)
	}
	if filter.StartTime != "" {
		whereClause += " AND created_at >= ?"
		args = append(args, filter.StartTime)
	}
	if filter.EndTime != "" {
		whereClause += " AND created_at <= ?"
		args = append(args, filter.EndTime)
	}

	// 查询总数
	var total int64
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM logs %s", whereClause)
	if err := s.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// 查询数据
	query := fmt.Sprintf(`
		SELECT id, api_key, network_id, peer_id, level, category, message, metadata, ip_address, user_agent, received_at
		FROM logs %s
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`, whereClause)
	args = append(args, filter.Limit, filter.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []*LogRequest
	for rows.Next() {
		var log LogRequest
		var metadata sql.NullString
		err := rows.Scan(
			&log.ID, &log.APIKey, &log.NetworkID, &log.PeerID,
			&log.Level, &log.Category, &log.Message, &metadata,
			&log.IPAddress, &log.UserAgent, &log.ReceivedAt,
		)
		if err != nil {
			return nil, 0, err
		}
		if metadata.Valid {
			log.Metadata = metadata.String
		}
		logs = append(logs, &log)
	}

	return logs, total, nil
}

// InsertAlert 插入告警
func (s *SQLiteStorage) InsertAlert(networkID, peerID, level, ruleName, message, details string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO alerts (network_id, peer_id, level, rule_name, message, details)
		VALUES (?, ?, ?, ?, ?, ?)
	`, networkID, peerID, level, ruleName, message, details)
	return err
}

// QueryAlerts 查询告警
func (s *SQLiteStorage) QueryAlerts(filter *AlertFilter) ([]*Alert, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	whereClause := "WHERE 1=1"
	args := []interface{}{}

	if filter.NetworkID != "" {
		whereClause += " AND network_id = ?"
		args = append(args, filter.NetworkID)
	}
	if filter.Level != "" {
		whereClause += " AND level = ?"
		args = append(args, filter.Level)
	}

	var total int64
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM alerts %s", whereClause)
	if err := s.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := fmt.Sprintf(`
		SELECT id, network_id, peer_id, level, rule_name, message, details, created_at, acknowledged, acknowledged_at, acknowledged_by
		FROM alerts %s
		ORDER BY created_at DESC
		LIMIT ?
	`, whereClause)
	args = append(args, filter.Limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var alerts []*Alert
	for rows.Next() {
		var alert Alert
		err := rows.Scan(
			&alert.ID, &alert.NetworkID, &alert.PeerID, &alert.Level,
			&alert.RuleName, &alert.Message, &alert.Details,
			&alert.CreatedAt, &alert.Acknowledged, &alert.AcknowledgedAt, &alert.AcknowledgedBy,
		)
		if err != nil {
			return nil, 0, err
		}
		alerts = append(alerts, &alert)
	}

	return alerts, total, nil
}

// Alert 告警结构
type Alert struct {
	ID             int64      `json:"id"`
	NetworkID      string     `json:"network_id"`
	PeerID         string     `json:"peer_id"`
	Level          string     `json:"level"`
	RuleName       string     `json:"rule_name"`
	Message        string     `json:"message"`
	Details        string     `json:"details"`
	CreatedAt      time.Time  `json:"created_at"`
	Acknowledged   int        `json:"acknowledged"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	AcknowledgedBy *string    `json:"acknowledged_by,omitempty"`
}

// AlertFilter 告警过滤器
type AlertFilter struct {
	NetworkID string
	Level     string
	Limit     int
}

// RecordTrafficAggregation 记录流量聚合
func (s *SQLiteStorage) RecordTrafficAggregation(networkID, period string, bytesSent, bytesRecv, packetCount int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var periodStart, periodEnd time.Time

	switch period {
	case "hour":
		periodStart = now.Truncate(time.Hour)
		periodEnd = periodStart.Add(time.Hour)
	case "day":
		periodStart = now.Truncate(24 * time.Hour)
		periodEnd = periodStart.Add(24 * time.Hour)
	case "month":
		periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		periodEnd = periodStart.AddDate(0, 1, 0)
	default:
		periodStart = now.Truncate(24 * time.Hour)
		periodEnd = periodStart.Add(24 * time.Hour)
	}

	_, err := s.db.Exec(`
		INSERT INTO traffic_aggregation (network_id, period, period_start, period_end, bytes_sent, bytes_recv, packet_count, record_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(network_id, period, period_start) DO UPDATE SET
			bytes_sent = bytes_sent + excluded.bytes_sent,
			bytes_recv = bytes_recv + excluded.bytes_recv,
			packet_count = packet_count + excluded.packet_count,
			record_count = record_count + 1
	`, networkID, period, periodStart, periodEnd, bytesSent, bytesRecv, packetCount)

	return err
}

// GetTrafficStats 获取流量统计
func (s *SQLiteStorage) GetTrafficStats(networkID, period string) ([]*TrafficStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var startTime time.Time
	now := time.Now()

	switch period {
	case "hour":
		startTime = now.AddDate(0, 0, -7) // 最近 7 天
	case "day":
		startTime = now.AddDate(0, 0, -30) // 最近 30 天
	case "month":
		startTime = now.AddDate(-1, 0, 0) // 最近 1 年
	default:
		startTime = now.AddDate(0, 0, -30)
	}

	query := `
		SELECT network_id, period, period_start, period_end, bytes_sent, bytes_recv, packet_count, record_count
		FROM traffic_aggregation
		WHERE period = ? AND period_start >= ?
	`
	args := []interface{}{period, startTime}

	if networkID != "" {
		query += " AND network_id = ?"
		args = append(args, networkID)
	}

	query += " ORDER BY period_start DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []*TrafficStats
	for rows.Next() {
		var s TrafficStats
		err := rows.Scan(&s.NetworkID, &s.Period, &s.StartTime, &s.EndTime, &s.BytesSent, &s.BytesRecv, &s.PacketCount, &s.RecordCount)
		if err != nil {
			return nil, err
		}
		stats = append(stats, &s)
	}

	return stats, nil
}

// StartAggregationTicker 启动聚合定时器
func (s *SQLiteStorage) StartAggregationTicker() {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s.aggregateTraffic()
			case <-s.stopCh:
				return
			}
		}
	}()
}

// aggregateTraffic 执行流量聚合
func (s *SQLiteStorage) aggregateTraffic() {
	log.Println("执行流量聚合...")

	// 查询最近 2 小时的所有日志进行聚合
	rows, err := s.db.Query(`
		SELECT network_id, COUNT(*), SUM(LENGTH(message)), 0
		FROM logs
		WHERE created_at >= datetime('now', '-2 hours')
		GROUP BY network_id
	`)
	if err != nil {
		log.Printf("聚合查询失败: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var networkID string
		var count, bytes int64
		if err := rows.Scan(&networkID, &count, &bytes, &count); err != nil {
			continue
		}
		if err := s.RecordTrafficAggregation(networkID, "hour", bytes, bytes/2, count); err != nil {
			log.Printf("记录聚合失败: %v", err)
		}
	}
}

// TrafficLogFromBody 从请求体解析流量日志
type TrafficLogFromBody struct {
	NetworkID  string `json:"network_id"`
	PeerID     string `json:"peer_id"`
	BytesSent  int64  `json:"bytes_sent"`
	BytesRecv  int64  `json:"bytes_recv"`
	Packets    int64  `json:"packets"`
	RecordedAt string `json:"recorded_at"`
}

// ParseMetadata 解析元数据 JSON
func ParseMetadata(metadata string) (map[string]interface{}, error) {
	var result map[string]interface{}
	if metadata == "" {
		return result, nil
	}
	err := json.Unmarshal([]byte(metadata), &result)
	return result, err
}
