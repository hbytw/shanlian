package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
)

type AuditRepository struct {
	db *PostgresDB
}

func NewAuditRepository(db *PostgresDB) *AuditRepository {
	return &AuditRepository{db: db}
}

// LogAdmin 操作日志
func (r *AuditRepository) LogAdmin(ctx context.Context, log *model.AdminLog) error {
	query := `
		INSERT INTO admin_logs (id, operator_id, operator_name, action, resource_type,
		                         resource_id, resource_name, detail, ip_address, user_agent, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
	`
	_, err := r.db.ExecContext(ctx, query,
		log.ID, log.OperatorID, log.OperatorName, log.Action, log.ResourceType,
		log.ResourceID, log.ResourceName, log.Detail, log.IPAddress, log.UserAgent,
	)
	if err != nil {
		return fmt.Errorf("记录管理员操作日志失败：%w", err)
	}
	return nil
}

// LogAccess 访问日志
func (r *AuditRepository) LogAccess(ctx context.Context, log *model.AccessLog) error {
	query := `
		INSERT INTO access_logs (peer_id, device_id, network_id, session_id, url, host, path,
		                         method, status_code, bytes_in, bytes_out, duration_ms, accessed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW())
	`
	_, err := r.db.ExecContext(ctx, query,
		log.PeerID, log.DeviceID, log.NetworkID, log.SessionID, log.URL, log.Host, log.Path,
		log.Method, log.StatusCode, log.BytesIn, log.BytesOut, log.DurationMs,
	)
	if err != nil {
		return fmt.Errorf("记录访问日志失败：%w", err)
	}
	return nil
}

// LogTraffic 流量日志
func (r *AuditRepository) LogTraffic(ctx context.Context, log *model.TrafficLog) error {
	query := `
		INSERT INTO traffic_logs (peer_id, session_id, network_id, direction, protocol, bytes,
		                          packets, duration_sec, tunnel_type, logged_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
	`
	_, err := r.db.ExecContext(ctx, query,
		log.PeerID, log.SessionID, log.NetworkID, log.Direction, log.Protocol,
		log.Bytes, log.Packets, log.DurationSec, log.TunnelType,
	)
	if err != nil {
		return fmt.Errorf("记录流量日志失败：%w", err)
	}
	return nil
}

// LogOnline 上下线日志
func (r *AuditRepository) LogOnline(ctx context.Context, log *model.OnlineLog) error {
	query := `
		INSERT INTO online_logs (peer_id, device_id, network_id, action, public_ip, hostname, duration_sec, logged_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
	`
	_, err := r.db.ExecContext(ctx, query,
		log.PeerID, log.DeviceID, log.NetworkID, log.Action, log.PublicIP, log.Hostname, log.DurationSec,
	)
	if err != nil {
		return fmt.Errorf("记录上下线日志失败：%w", err)
	}
	return nil
}

// GetAccessLogs 获取访问日志
func (r *AuditRepository) GetAccessLogs(ctx context.Context, params *model.PageParams) ([]model.AccessLog, int64, error) {
	countQuery := `SELECT COUNT(*) FROM access_logs WHERE 1=1`
	listQuery := `SELECT id, peer_id, device_id, network_id, session_id, url, host, path,
	              method, status_code, bytes_in, bytes_out, duration_ms, accessed_at
	              FROM access_logs WHERE 1=1`

	args := []interface{}{}
	argIdx := 1

	if params.Search != "" {
		searchPattern := "%" + params.Search + "%"
		countQuery += fmt.Sprintf(" AND (url LIKE $%d OR host LIKE $%d)", argIdx, argIdx)
		listQuery += fmt.Sprintf(" AND (url LIKE $%d OR host LIKE $%d)", argIdx, argIdx)
		args = append(args, searchPattern)
		argIdx++
	}

	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计访问日志数量失败：%w", err)
	}

	listQuery += fmt.Sprintf(" ORDER BY accessed_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.GetOffset())

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询访问日志列表失败：%w", err)
	}
	defer rows.Close()

	var logs []model.AccessLog
	for rows.Next() {
		var log model.AccessLog
		err := rows.Scan(
			&log.ID, &log.PeerID, &log.DeviceID, &log.NetworkID, &log.SessionID,
			&log.URL, &log.Host, &log.Path, &log.Method, &log.StatusCode,
			&log.BytesIn, &log.BytesOut, &log.DurationMs, &log.AccessedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("扫描访问日志行失败：%w", err)
		}
		logs = append(logs, log)
	}

	return logs, total, nil
}

// GetTrafficLogs 获取流量日志
func (r *AuditRepository) GetTrafficLogs(ctx context.Context, params *model.PageParams) ([]model.TrafficLog, int64, error) {
	countQuery := `SELECT COUNT(*) FROM traffic_logs WHERE 1=1`
	listQuery := `SELECT id, peer_id, session_id, network_id, direction, protocol, bytes,
	              packets, duration_sec, tunnel_type, logged_at
	              FROM traffic_logs WHERE 1=1`

	args := []interface{}{}
	argIdx := 1

	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计流量日志数量失败：%w", err)
	}

	listQuery += fmt.Sprintf(" ORDER BY logged_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.GetOffset())

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询流量日志列表失败：%w", err)
	}
	defer rows.Close()

	var logs []model.TrafficLog
	for rows.Next() {
		var log model.TrafficLog
		err := rows.Scan(
			&log.ID, &log.PeerID, &log.SessionID, &log.NetworkID, &log.Direction,
			&log.Protocol, &log.Bytes, &log.Packets, &log.DurationSec, &log.TunnelType, &log.LoggedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("扫描流量日志行失败：%w", err)
		}
		logs = append(logs, log)
	}

	return logs, total, nil
}

// GetAdminLogs 获取管理员操作日志
func (r *AuditRepository) GetAdminLogs(ctx context.Context, params *model.PageParams) ([]model.AdminLog, int64, error) {
	countQuery := `SELECT COUNT(*) FROM admin_logs WHERE 1=1`
	listQuery := `SELECT id, operator_id, operator_name, action, resource_type, resource_id,
	              resource_name, detail, ip_address, user_agent, created_at
	              FROM admin_logs WHERE 1=1`

	args := []interface{}{}
	argIdx := 1

	if params.Search != "" {
		searchPattern := "%" + params.Search + "%"
		countQuery += fmt.Sprintf(" AND (operator_name LIKE $%d OR action LIKE $%d)", argIdx, argIdx)
		listQuery += fmt.Sprintf(" AND (operator_name LIKE $%d OR action LIKE $%d)", argIdx, argIdx)
		args = append(args, searchPattern)
		argIdx++
	}

	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计管理员日志数量失败：%w", err)
	}

	listQuery += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.GetOffset())

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询管理员日志列表失败：%w", err)
	}
	defer rows.Close()

	var logs []model.AdminLog
	for rows.Next() {
		var log model.AdminLog
		var detailBytes []byte
		err := rows.Scan(
			&log.ID, &log.OperatorID, &log.OperatorName, &log.Action, &log.ResourceType,
			&log.ResourceID, &log.ResourceName, &detailBytes, &log.IPAddress, &log.UserAgent, &log.CreatedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("扫描管理员日志行失败：%w", err)
		}
		log.Detail = json.RawMessage(detailBytes)
		logs = append(logs, log)
	}

	return logs, total, nil
}

// GetOnlineLogs 获取上下线日志
func (r *AuditRepository) GetOnlineLogs(ctx context.Context, params *model.PageParams) ([]model.OnlineLog, int64, error) {
	countQuery := `SELECT COUNT(*) FROM online_logs WHERE 1=1`
	listQuery := `SELECT id, peer_id, device_id, network_id, action, public_ip, hostname, duration_sec, logged_at
	              FROM online_logs WHERE 1=1`

	args := []interface{}{}
	argIdx := 1

	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计上下线日志数量失败：%w", err)
	}

	listQuery += fmt.Sprintf(" ORDER BY logged_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.GetOffset())

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询上下线日志列表失败：%w", err)
	}
	defer rows.Close()

	var logs []model.OnlineLog
	for rows.Next() {
		var log model.OnlineLog
		err := rows.Scan(
			&log.ID, &log.PeerID, &log.DeviceID, &log.NetworkID, &log.Action,
			&log.PublicIP, &log.Hostname, &log.DurationSec, &log.LoggedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("扫描上下线日志行失败：%w", err)
		}
		logs = append(logs, log)
	}

	return logs, total, nil
}

// GetPolicyChangeLogs 获取策略变更日志
func (r *AuditRepository) GetPolicyChangeLogs(ctx context.Context, params *model.PageParams) ([]model.PolicyChangeLog, int64, error) {
	countQuery := `SELECT COUNT(*) FROM policy_change_logs WHERE 1=1`
	listQuery := `SELECT id, peer_id, device_id, network_id, policy_id, policy_name, policy_type,
	              action, old_config, new_config, reason, operator, operator_id, changed_at
	              FROM policy_change_logs WHERE 1=1`

	args := []interface{}{}
	argIdx := 1

	var total int64
	err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("统计策略变更日志数量失败：%w", err)
	}

	listQuery += fmt.Sprintf(" ORDER BY changed_at DESC LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, params.Limit, params.GetOffset())

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询策略变更日志列表失败：%w", err)
	}
	defer rows.Close()

	var logs []model.PolicyChangeLog
	for rows.Next() {
		var log model.PolicyChangeLog
		var oldConfig, newConfig []byte
		err := rows.Scan(
			&log.ID, &log.PeerID, &log.DeviceID, &log.NetworkID, &log.PolicyID,
			&log.PolicyName, &log.PolicyType, &log.Action, &oldConfig, &newConfig,
			&log.Reason, &log.Operator, &log.OperatorID, &log.ChangedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("扫描策略变更日志行失败：%w", err)
		}
		log.OldConfig = oldConfig
		log.NewConfig = newConfig
		logs = append(logs, log)
	}

	return logs, total, nil
}

// GetTrafficSummary 获取流量汇总
func (r *AuditRepository) GetTrafficSummary(ctx context.Context, networkID, peerID *uuid.UUID, startTime, endTime time.Time) (int64, int64, error) {
	query := `
		SELECT COALESCE(SUM(CASE WHEN direction = 'in' THEN bytes ELSE 0 END), 0) as traffic_in,
		       COALESCE(SUM(CASE WHEN direction = 'out' THEN bytes ELSE 0 END), 0) as traffic_out
		FROM traffic_logs
		WHERE logged_at >= $1 AND logged_at <= $2
	`
	args := []interface{}{startTime, endTime}
	argIdx := 3

	if networkID != nil {
		query += fmt.Sprintf(" AND network_id = $%d", argIdx)
		args = append(args, *networkID)
		argIdx++
	}
	if peerID != nil {
		query += fmt.Sprintf(" AND peer_id = $%d", argIdx)
		args = append(args, *peerID)
	}

	var trafficIn, trafficOut int64
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&trafficIn, &trafficOut)
	if err != nil {
		return 0, 0, fmt.Errorf("获取流量汇总失败：%w", err)
	}
	return trafficIn, trafficOut, nil
}

// GetTopPeersTraffic 获取 Top 节点流量
func (r *AuditRepository) GetTopPeersTraffic(ctx context.Context, networkID *uuid.UUID, startTime, endTime time.Time, limit int) ([]model.TrafficItem, error) {
	query := `
		SELECT p.id, COALESCE(SUM(t.bytes), 0) as total_bytes
		FROM peers p
		LEFT JOIN traffic_logs t ON p.id = t.peer_id AND t.logged_at >= $1 AND t.logged_at <= $2
		WHERE 1=1
	`
	args := []interface{}{startTime, endTime}
	argIdx := 3

	if networkID != nil {
		query += fmt.Sprintf(" AND p.network_id = $%d", argIdx)
		args = append(args, *networkID)
		argIdx++
	}

	query += fmt.Sprintf(" GROUP BY p.id ORDER BY total_bytes DESC LIMIT $%d", argIdx)
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("获取 Top 节点流量失败：%w", err)
	}
	defer rows.Close()

	var items []model.TrafficItem
	for rows.Next() {
		var item model.TrafficItem
		err := rows.Scan(&item.ID, &item.TrafficIn)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// GetTopNetworksTraffic 获取 Top 网络流量
func (r *AuditRepository) GetTopNetworksTraffic(ctx context.Context, startTime, endTime time.Time, limit int) ([]model.TrafficItem, error) {
	query := `
		SELECT n.id, n.name, COALESCE(SUM(t.bytes), 0) as total_bytes
		FROM networks n
		LEFT JOIN traffic_logs t ON n.id = t.network_id AND t.logged_at >= $1 AND t.logged_at <= $2
		GROUP BY n.id, n.name
		ORDER BY total_bytes DESC
		LIMIT $3
	`

	rows, err := r.db.QueryContext(ctx, query, startTime, endTime, limit)
	if err != nil {
		return nil, fmt.Errorf("获取 Top 网络流量失败：%w", err)
	}
	defer rows.Close()

	var items []model.TrafficItem
	for rows.Next() {
		var item model.TrafficItem
		err := rows.Scan(&item.ID, &item.Name, &item.TrafficIn)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// GetTrafficTimeline 获取流量时间线
func (r *AuditRepository) GetTrafficTimeline(ctx context.Context, networkID, peerID *uuid.UUID, startTime, endTime time.Time, groupBy string) ([]model.TrafficPoint, error) {
	var truncUnit string
	switch groupBy {
	case "hour":
		truncUnit = "hour"
	case "day":
		truncUnit = "day"
	case "month":
		truncUnit = "month"
	default:
		truncUnit = "hour"
	}

	query := fmt.Sprintf(`
		SELECT DATE_TRUNC('%s', logged_at) as time_bucket,
		       COALESCE(SUM(CASE WHEN direction = 'in' THEN bytes ELSE 0 END), 0) as traffic_in,
		       COALESCE(SUM(CASE WHEN direction = 'out' THEN bytes ELSE 0 END), 0) as traffic_out
		FROM traffic_logs
		WHERE logged_at >= $1 AND logged_at <= $2
	`, truncUnit)

	args := []interface{}{startTime, endTime}
	argIdx := 3

	if networkID != nil {
		query += fmt.Sprintf(" AND network_id = $%d", argIdx)
		args = append(args, *networkID)
		argIdx++
	}
	if peerID != nil {
		query += fmt.Sprintf(" AND peer_id = $%d", argIdx)
		args = append(args, *peerID)
	}

	query += fmt.Sprintf(" GROUP BY time_bucket ORDER BY time_bucket")

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("获取流量时间线失败：%w", err)
	}
	defer rows.Close()

	var timeline []model.TrafficPoint
	for rows.Next() {
		var point model.TrafficPoint
		err := rows.Scan(&point.Timestamp, &point.TrafficIn, &point.TrafficOut)
		if err != nil {
			return nil, err
		}
		timeline = append(timeline, point)
	}
	return timeline, nil
}

// GetTopPeersByTraffic 按流量获取 Top 节点
func (r *AuditRepository) GetTopPeersByTraffic(ctx context.Context, startTime, endTime time.Time, limit int) ([]model.TopPeer, error) {
	query := `
		SELECT p.id, p.device_id, p.hostname, COALESCE(SUM(t.bytes), 0) as total_bytes
		FROM peers p
		LEFT JOIN traffic_logs t ON p.id = t.peer_id AND t.logged_at >= $1 AND t.logged_at <= $2
		GROUP BY p.id, p.device_id, p.hostname
		ORDER BY total_bytes DESC
		LIMIT $3
	`

	rows, err := r.db.QueryContext(ctx, query, startTime, endTime, limit)
	if err != nil {
		return nil, fmt.Errorf("按流量获取 Top 节点失败：%w", err)
	}
	defer rows.Close()

	var peers []model.TopPeer
	for rows.Next() {
		var peer model.TopPeer
		err := rows.Scan(&peer.PeerID, &peer.DeviceID, &peer.Hostname, &peer.TrafficIn)
		if err != nil {
			return nil, err
		}
		peers = append(peers, peer)
	}
	return peers, nil
}

// GetTopNetworksByTraffic 按流量获取 Top 网络
func (r *AuditRepository) GetTopNetworksByTraffic(ctx context.Context, startTime, endTime time.Time, limit int) ([]model.TopNetwork, error) {
	query := `
		SELECT n.id, n.name, COUNT(p.id) as peer_count, COALESCE(SUM(t.bytes), 0) as total_bytes
		FROM networks n
		LEFT JOIN peers p ON n.id = p.network_id
		LEFT JOIN traffic_logs t ON n.id = t.network_id AND t.logged_at >= $1 AND t.logged_at <= $2
		GROUP BY n.id, n.name
		ORDER BY total_bytes DESC
		LIMIT $3
	`

	rows, err := r.db.QueryContext(ctx, query, startTime, endTime, limit)
	if err != nil {
		return nil, fmt.Errorf("按流量获取 Top 网络失败：%w", err)
	}
	defer rows.Close()

	var networks []model.TopNetwork
	for rows.Next() {
		var network model.TopNetwork
		err := rows.Scan(&network.NetworkID, &network.NetworkName, &network.PeerCount, &network.TrafficIn)
		if err != nil {
			return nil, err
		}
		networks = append(networks, network)
	}
	return networks, nil
}

// GetUserStats 获取用户统计
func (r *AuditRepository) GetUserStats(ctx context.Context) ([]model.User, int64, error) {
	query := `SELECT id, username, email, phone, password_hash, role, enabled, created_at, updated_at FROM users LIMIT 100`
	countQuery := `SELECT COUNT(*) FROM users`

	var users []model.User
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	for rows.Next() {
		var user model.User
		err := rows.Scan(&user.ID, &user.Username, &user.Email, &user.Phone, &user.PasswordHash, &user.Role, &user.Enabled, &user.CreatedAt, &user.UpdatedAt)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, user)
	}

	var total int64
	r.db.QueryRowContext(ctx, countQuery).Scan(&total)

	return users, total, nil
}

// GetDeviceStats 获取设备统计
func (r *AuditRepository) GetDeviceStats(ctx context.Context) ([]model.Device, int64, error) {
	query := `SELECT id, device_id, user_id, hostname, public_ip, nat_type, assigned_ip, online, client_version, os_type, os_version, first_seen_at, last_seen_at, enabled, created_at, updated_at FROM devices LIMIT 100`
	countQuery := `SELECT COUNT(*) FROM devices`

	var devices []model.Device
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	for rows.Next() {
		var device model.Device
		err := rows.Scan(&device.ID, &device.DeviceID, &device.UserID, &device.Hostname, &device.PublicIP, &device.NATType, &device.AssignedIP, &device.Online, &device.ClientVersion, &device.OSType, &device.OSVersion, &device.FirstSeenAt, &device.LastSeenAt, &device.Enabled, &device.CreatedAt, &device.UpdatedAt)
		if err != nil {
			return nil, 0, err
		}
		devices = append(devices, device)
	}

	var total int64
	r.db.QueryRowContext(ctx, countQuery).Scan(&total)

	return devices, total, nil
}

// CountAccessLogs 统计访问日志数量
func (r *AuditRepository) CountAccessLogs(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_logs`).Scan(&count)
	return count, err
}

// CountTrafficLogs 统计流量日志数量
func (r *AuditRepository) CountTrafficLogs(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM traffic_logs`).Scan(&count)
	return count, err
}

// CountOnlineLogs 统计上下线日志数量
func (r *AuditRepository) CountOnlineLogs(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM online_logs`).Scan(&count)
	return count, err
}

// CountAdminLogs 统计管理员日志数量
func (r *AuditRepository) CountAdminLogs(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_logs`).Scan(&count)
	return count, err
}

// CountPolicyChangeLogs 统计策略变更日志数量
func (r *AuditRepository) CountPolicyChangeLogs(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM policy_change_logs`).Scan(&count)
	return count, err
}

// CreateAdminLogID 生成管理员日志ID
func (r *AuditRepository) CreateAdminLogID() uuid.UUID {
	return uuid.New()
}

// RecordTrafficAggregation 记录流量聚合结果
func (r *AuditRepository) RecordTrafficAggregation(ctx context.Context, agg *model.TrafficAggregation) error {
	query := `
		INSERT INTO traffic_aggregation (id, network_id, period, period_key, bytes_in, bytes_out, record_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (network_id, period, period_key) DO UPDATE SET bytes_in = $5, bytes_out = $6, record_at = $7
	`
	_, err := r.db.ExecContext(ctx, query, uuid.New(), agg.NetworkID, agg.Period, agg.PeriodKey, agg.BytesIn, agg.BytesOut, agg.RecordAt)
	if err != nil {
		return fmt.Errorf("记录流量聚合失败：%w", err)
	}
	return nil
}

// CheckTrafficQuotaAndAlert 检查流量配额并写入告警
func (r *AuditRepository) CheckTrafficQuotaAndAlert(ctx context.Context, networkID uuid.UUID, monthlyBytes int64) error {
	// 查找网络的流量配额（从 network 表或单独的配置表）
	// 这里简化处理，使用默认配额：每月 100GB
	const defaultMonthlyQuotaBytes = 100 * 1024 * 1024 * 1024 // 100GB

	alertThreshold := 0.8 // 80% 阈值

	if monthlyBytes > int64(float64(defaultMonthlyQuotaBytes)*alertThreshold) {
		// 写入告警日志
		detail, _ := json.Marshal(map[string]interface{}{
			"network_id":   networkID,
			"monthly_bytes": monthlyBytes,
			"quota_bytes":  defaultMonthlyQuotaBytes,
			"usage_percent": float64(monthlyBytes) / float64(defaultMonthlyQuotaBytes) * 100,
		})
		alertLog := &model.AdminLog{
			ID:           uuid.New(),
			Action:       "流量配额告警",
			ResourceType: "network",
			ResourceID:   &networkID,
			Detail:       detail,
		}
		if err := r.LogAdmin(ctx, alertLog); err != nil {
			return fmt.Errorf("写入流量告警失败：%w", err)
		}
	}
	return nil
}

// GetMonthlyTraffic 获取月度流量
func (r *AuditRepository) GetMonthlyTraffic(ctx context.Context, networkID uuid.UUID) (int64, error) {
	// 获取本月初时间
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	query := `
		SELECT COALESCE(SUM(bytes_in + bytes_out), 0)
		FROM traffic_logs
		WHERE network_id = $1 AND logged_at >= $2
	`
	var totalBytes int64
	err := r.db.QueryRowContext(ctx, query, networkID, monthStart).Scan(&totalBytes)
	if err != nil {
		return 0, fmt.Errorf("获取月度流量失败：%w", err)
	}
	return totalBytes, nil
}
