package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
)

// AuditService 审计服务
type AuditService struct {
	auditRepo *repository.AuditRepository
}

func NewAuditService(auditRepo *repository.AuditRepository) *AuditService {
	return &AuditService{
		auditRepo: auditRepo,
	}
}

// AccessLogs 获取访问日志
func (s *AuditService) AccessLogs(ctx context.Context, params *model.PageParams) ([]model.AccessLog, int64, error) {
	return s.auditRepo.GetAccessLogs(ctx, params)
}

// TrafficLogs 获取流量日志
func (s *AuditService) TrafficLogs(ctx context.Context, params *model.PageParams) ([]model.TrafficLog, int64, error) {
	return s.auditRepo.GetTrafficLogs(ctx, params)
}

// PolicyChangeLogs 获取策略变更日志
func (s *AuditService) PolicyChangeLogs(ctx context.Context, params *model.PageParams) ([]model.PolicyChangeLog, int64, error) {
	return s.auditRepo.GetPolicyChangeLogs(ctx, params)
}

// OnlineLogs 获取上下线日志
func (s *AuditService) OnlineLogs(ctx context.Context, params *model.PageParams) ([]model.OnlineLog, int64, error) {
	return s.auditRepo.GetOnlineLogs(ctx, params)
}

// AdminLogs 获取管理员操作日志
func (s *AuditService) AdminLogs(ctx context.Context, params *model.PageParams) ([]model.AdminLog, int64, error) {
	return s.auditRepo.GetAdminLogs(ctx, params)
}

// ExportFormat 导出格式
type ExportFormat string

const (
	ExportFormatCSV   ExportFormat = "csv"
	ExportFormatJSON  ExportFormat = "json"
)

// ExportLogType 导出日志类型
type ExportLogType string

const (
	ExportLogTypeAccess  ExportLogType = "access"
	ExportLogTypeTraffic ExportLogType = "traffic"
	ExportLogTypeOnline  ExportLogType = "online"
	ExportLogTypeAdmin   ExportLogType = "admin"
	ExportLogTypePolicy  ExportLogType = "policy"
)

// ExportOptions 导出选项
type ExportOptions struct {
	LogType ExportLogType
	Format  ExportFormat
	StartTime *time.Time
	EndTime   *time.Time
}

// Export 导出日志
func (s *AuditService) Export(ctx context.Context, params *model.PageParams, opts *ExportOptions) ([]byte, error) {
	if opts == nil {
		opts = &ExportOptions{
			LogType: ExportLogTypeAdmin,
			Format:  ExportFormatJSON,
		}
	}

	var data interface{}
	var err error

	switch opts.LogType {
	case ExportLogTypeAccess:
		data, _, err = s.auditRepo.GetAccessLogs(ctx, params)
	case ExportLogTypeTraffic:
		data, _, err = s.auditRepo.GetTrafficLogs(ctx, params)
	case ExportLogTypeOnline:
		data, _, err = s.auditRepo.GetOnlineLogs(ctx, params)
	case ExportLogTypeAdmin:
		data, _, err = s.auditRepo.GetAdminLogs(ctx, params)
	case ExportLogTypePolicy:
		data, _, err = s.auditRepo.GetPolicyChangeLogs(ctx, params)
	default:
		return nil, fmt.Errorf("不支持的日志类型：%s", opts.LogType)
	}

	if err != nil {
		return nil, err
	}

	switch opts.Format {
	case ExportFormatCSV:
		return s.exportToCSV(data, opts.LogType)
	case ExportFormatJSON:
		return s.exportToJSON(data)
	default:
		return nil, fmt.Errorf("不支持的导出格式：%s", opts.Format)
	}
}

// exportToJSON 导出为 JSON 格式
func (s *AuditService) exportToJSON(data interface{}) ([]byte, error) {
	exportData := map[string]interface{}{
		"exported_at": time.Now().Format(time.RFC3339),
		"data":        data,
	}
	return json.MarshalIndent(exportData, "", "  ")
}

// exportToCSV 导出为 CSV 格式
func (s *AuditService) exportToCSV(data interface{}, logType ExportLogType) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	// 根据日志类型写入表头
	headers := s.getCSVHeaders(logType)
	if err := writer.Write(headers); err != nil {
		return nil, err
	}

	// 根据数据类型写入数据
	switch v := data.(type) {
	case []model.AccessLog:
		for _, log := range v {
			record := s.accessLogToRecord(log)
			if err := writer.Write(record); err != nil {
				return nil, err
			}
		}
	case []model.TrafficLog:
		for _, log := range v {
			record := s.trafficLogToRecord(log)
			if err := writer.Write(record); err != nil {
				return nil, err
			}
		}
	case []model.OnlineLog:
		for _, log := range v {
			record := s.onlineLogToRecord(log)
			if err := writer.Write(record); err != nil {
				return nil, err
			}
		}
	case []model.AdminLog:
		for _, log := range v {
			record := s.adminLogToRecord(log)
			if err := writer.Write(record); err != nil {
				return nil, err
			}
		}
	case []model.PolicyChangeLog:
		for _, log := range v {
			record := s.policyChangeLogToRecord(log)
			if err := writer.Write(record); err != nil {
				return nil, err
			}
		}
	}

	writer.Flush()
	return buf.Bytes(), nil
}

// getCSVHeaders 获取 CSV 表头
func (s *AuditService) getCSVHeaders(logType ExportLogType) []string {
	switch logType {
	case ExportLogTypeAccess:
		return []string{"ID", "PeerID", "DeviceID", "NetworkID", "SessionID", "URL", "Host", "Path", "Method", "StatusCode", "BytesIn", "BytesOut", "DurationMs", "AccessedAt"}
	case ExportLogTypeTraffic:
		return []string{"ID", "PeerID", "SessionID", "NetworkID", "Direction", "Protocol", "Bytes", "Packets", "DurationSec", "TunnelType", "LoggedAt"}
	case ExportLogTypeOnline:
		return []string{"ID", "PeerID", "DeviceID", "NetworkID", "Action", "PublicIP", "Hostname", "DurationSec", "LoggedAt"}
	case ExportLogTypeAdmin:
		return []string{"ID", "OperatorID", "OperatorName", "Action", "ResourceType", "ResourceID", "ResourceName", "IPAddress", "UserAgent", "CreatedAt"}
	case ExportLogTypePolicy:
		return []string{"ID", "PeerID", "DeviceID", "NetworkID", "PolicyID", "PolicyName", "PolicyType", "Action", "Reason", "Operator", "OperatorID", "ChangedAt"}
	default:
		return nil
	}
}

func (s *AuditService) accessLogToRecord(log model.AccessLog) []string {
	return []string{
		fmt.Sprintf("%d", log.ID),
		log.PeerID.String(),
		log.DeviceID,
		log.NetworkID.String(),
		fmt.Sprintf("%s", ptrToStringID(log.SessionID)),
		log.URL,
		ptrToString(log.Host),
		ptrToString(log.Path),
		ptrToString(log.Method),
		fmt.Sprintf("%d", ptrToInt(log.StatusCode)),
		fmt.Sprintf("%d", log.BytesIn),
		fmt.Sprintf("%d", log.BytesOut),
		fmt.Sprintf("%d", ptrToInt(log.DurationMs)),
		log.AccessedAt.Format(time.RFC3339),
	}
}

func (s *AuditService) trafficLogToRecord(log model.TrafficLog) []string {
	sessionID := ""
	if log.SessionID != nil {
		sessionID = log.SessionID.String()
	}
	return []string{
		fmt.Sprintf("%d", log.ID),
		log.PeerID.String(),
		sessionID,
		log.NetworkID.String(),
		log.Direction,
		log.Protocol,
		fmt.Sprintf("%d", log.Bytes),
		fmt.Sprintf("%d", log.Packets),
		fmt.Sprintf("%d", log.DurationSec),
		log.TunnelType,
		log.LoggedAt.Format(time.RFC3339),
	}
}

func (s *AuditService) onlineLogToRecord(log model.OnlineLog) []string {
	return []string{
		fmt.Sprintf("%d", log.ID),
		log.PeerID.String(),
		log.DeviceID,
		log.NetworkID.String(),
		log.Action,
		ptrToString(log.PublicIP),
		ptrToString(log.Hostname),
		fmt.Sprintf("%d", ptrToInt(log.DurationSec)),
		log.LoggedAt.Format(time.RFC3339),
	}
}

func (s *AuditService) adminLogToRecord(log model.AdminLog) []string {
	return []string{
		log.ID.String(),
		log.OperatorID.String(),
		ptrToString(log.OperatorName),
		log.Action,
		log.ResourceType,
		fmt.Sprintf("%s", ptrToStringID(log.ResourceID)),
		ptrToString(log.ResourceName),
		ptrToString(log.IPAddress),
		ptrToString(log.UserAgent),
		log.CreatedAt.Format(time.RFC3339),
	}
}

func (s *AuditService) policyChangeLogToRecord(log model.PolicyChangeLog) []string {
	peerID := ""
	if log.PeerID != nil {
		peerID = log.PeerID.String()
	}
	deviceID := ""
	if log.DeviceID != nil {
		deviceID = *log.DeviceID
	}
	return []string{
		fmt.Sprintf("%d", log.ID),
		peerID,
		deviceID,
		log.NetworkID.String(),
		log.PolicyID.String(),
		ptrToString(log.PolicyName),
		ptrToString(log.PolicyType),
		log.Action,
		ptrToString(log.Reason),
		ptrToString(log.Operator),
		fmt.Sprintf("%s", ptrToStringID(log.OperatorID)),
		log.ChangedAt.Format(time.RFC3339),
	}
}

// ptrToString 将 *string 转换为 string
func ptrToString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ptrToInt 将 *int 转换为 string
func ptrToInt(i *int) string {
	if i == nil {
		return ""
	}
	return fmt.Sprintf("%d", *i)
}

// ptrToStringID 将 *uuid.UUID 转换为 string
func ptrToStringID(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// AuditStatus 获取审计状态
func (s *AuditService) AuditStatus(ctx context.Context) (map[string]interface{}, error) {
	// 获取各类型日志数量
	accessCount, _ := s.auditRepo.CountAccessLogs(ctx)
	trafficCount, _ := s.auditRepo.CountTrafficLogs(ctx)
	onlineCount, _ := s.auditRepo.CountOnlineLogs(ctx)
	adminCount, _ := s.auditRepo.CountAdminLogs(ctx)
	policyCount, _ := s.auditRepo.CountPolicyChangeLogs(ctx)

	return map[string]interface{}{
		"status":          "running",
		"access_logs":     accessCount,
		"traffic_logs":    trafficCount,
		"online_logs":     onlineCount,
		"admin_logs":      adminCount,
		"policy_logs":     policyCount,
		"last_updated":    time.Now().Format(time.RFC3339),
	}, nil
}
