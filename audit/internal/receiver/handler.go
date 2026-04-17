package receiver

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/shanlian/audit/internal/alert"
	"github.com/shanlian/audit/internal/forward"
	"github.com/shanlian/audit/internal/storage"
	"github.com/valyala/fasthttp"
)

// Handler HTTP 处理器
type Handler struct {
	store       *storage.SQLiteStorage
	alertEngine *alert.Engine
	forwardMgr  *forward.Manager
}

// NewHandler 创建处理器
func NewHandler(store *storage.SQLiteStorage, alertEngine *alert.Engine, forwardMgr *forward.Manager) *Handler {
	return &Handler{
		store:       store,
		alertEngine: alertEngine,
		forwardMgr:  forwardMgr,
	}
}

// Handle 请求处理
func (h *Handler) Handle(ctx *fasthttp.RequestCtx) {
	path := string(ctx.Path())
	method := string(ctx.Method())

	// CORS 头
	ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")
	ctx.Response.Header.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	ctx.Response.Header.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")

	if method == "OPTIONS" {
		ctx.SetStatusCode(fasthttp.StatusOK)
		return
	}

	switch {
	case path == "/health":
		h.handleHealth(ctx)
	case path == "/api/v1/audit/log" && method == "POST":
		h.handleLog(ctx)
	case path == "/api/v1/audit/logs" && method == "GET":
		h.handleQueryLogs(ctx)
	case path == "/api/v1/audit/stats" && method == "GET":
		h.handleStats(ctx)
	case path == "/api/v1/audit/alert" && method == "GET":
		h.handleAlerts(ctx)
	case path == "/api/v1/audit/forward" && method == "GET":
		h.handleForwardList(ctx)
	case path == "/api/v1/audit/forward" && method == "POST":
		h.handleForwardCreate(ctx)
	default:
		ctx.SetStatusCode(fasthttp.StatusNotFound)
		jsonResponse(ctx, fasthttp.StatusNotFound, "not_found", "端点不存在")
	}
}

// 健康检查
func (h *Handler) handleHealth(ctx *fasthttp.RequestCtx) {
	jsonResponse(ctx, fasthttp.StatusOK, "healthy", map[string]interface{}{
		"timestamp": time.Now().Unix(),
	})
}

// 日志接收
func (h *Handler) handleLog(ctx *fasthttp.RequestCtx) {
	// 验证 API Key
	apiKey := string(ctx.Request.Header.Peek("X-API-Key"))
	if apiKey == "" {
		apiKey = string(ctx.Request.Header.Peek("Authorization"))
		if len(apiKey) > 7 && apiKey[:7] == "Bearer " {
			apiKey = apiKey[7:]
		}
	}

	// TODO: 验证 API Key (从数据库或配置)

	var req storage.LogRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		jsonResponse(ctx, fasthttp.StatusBadRequest, "invalid_request", "请求格式错误")
		return
	}

	req.APIKey = apiKey
	req.ReceivedAt = time.Now()

	if err := h.store.InsertLog(&req); err != nil {
		log.Printf("插入日志失败: %v", err)
		jsonResponse(ctx, fasthttp.StatusInternalServerError, "insert_failed", "存储日志失败")
		return
	}

	// 触发告警检查
	h.alertEngine.CheckLog(&req)

	jsonResponse(ctx, fasthttp.StatusOK, "ok", map[string]interface{}{
		"log_id": req.ID,
	})
}

// 查询日志
func (h *Handler) handleQueryLogs(ctx *fasthttp.RequestCtx) {
	query := ctx.QueryArgs()

	filter := storage.LogFilter{
		NetworkID:  string(query.Peek("network_id")),
		PeerID:     string(query.Peek("peer_id")),
		Level:      string(query.Peek("level")),
		StartTime:  string(query.Peek("start_time")),
		EndTime:    string(query.Peek("end_time")),
		Limit:      100,
		Offset:     0,
	}

	if limit := query.GetUintOrZero("limit"); limit > 0 && limit <= 1000 {
		filter.Limit = limit
	}
	if offset := query.GetUintOrZero("offset"); offset > 0 {
		filter.Offset = offset
	}

	logs, total, err := h.store.QueryLogs(&filter)
	if err != nil {
		jsonResponse(ctx, fasthttp.StatusInternalServerError, "query_failed", "查询失败")
		return
	}

	jsonResponse(ctx, fasthttp.StatusOK, "ok", map[string]interface{}{
		"logs":   logs,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

// 统计查询
func (h *Handler) handleStats(ctx *fasthttp.RequestCtx) {
	networkID := string(ctx.QueryArgs().Peek("network_id"))
	period := string(ctx.QueryArgs().Peek("period")) // hour, day, month

	if period == "" {
		period = "day"
	}

	stats, err := h.store.GetTrafficStats(networkID, period)
	if err != nil {
		jsonResponse(ctx, fasthttp.StatusInternalServerError, "query_failed", "查询失败")
		return
	}

	jsonResponse(ctx, fasthttp.StatusOK, "ok", stats)
}

// 告警查询
func (h *Handler) handleAlerts(ctx *fasthttp.RequestCtx) {
	query := ctx.QueryArgs()

	filter := alert.AlertFilter{
		NetworkID: string(query.Peek("network_id")),
		Level:     string(query.Peek("level")),
		Limit:     100,
	}

	if limit := query.GetUintOrZero("limit"); limit > 0 {
		filter.Limit = limit
	}

	alerts, err := h.alertEngine.QueryAlerts(&filter)
	if err != nil {
		jsonResponse(ctx, fasthttp.StatusInternalServerError, "query_failed", "查询失败")
		return
	}

	jsonResponse(ctx, fasthttp.StatusOK, "ok", alerts)
}

// 转发列表
func (h *Handler) handleForwardList(ctx *fasthttp.RequestCtx) {
	list, err := h.forwardMgr.List()
	if err != nil {
		jsonResponse(ctx, fasthttp.StatusInternalServerError, "query_failed", "查询失败")
		return
	}

	jsonResponse(ctx, fasthttp.StatusOK, "ok", list)
}

// 创建转发规则
func (h *Handler) handleForwardCreate(ctx *fasthttp.RequestCtx) {
	var req forward.ForwardRule
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		jsonResponse(ctx, fasthttp.StatusBadRequest, "invalid_request", "请求格式错误")
		return
	}

	if err := h.forwardMgr.Create(&req); err != nil {
		jsonResponse(ctx, fasthttp.StatusInternalServerError, "create_failed", "创建失败")
		return
	}

	jsonResponse(ctx, fasthttp.StatusOK, "ok", req)
}

func jsonResponse(ctx *fasthttp.RequestCtx, code int, status string, data interface{}) {
	ctx.SetStatusCode(code)
	resp := map[string]interface{}{
		"status": status,
	}
	if data != nil {
		resp["data"] = data
	}
	body, _ := json.Marshal(resp)
	ctx.SetContentType("application/json")
	ctx.Write(body)
}
