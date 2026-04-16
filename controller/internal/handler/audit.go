package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/service"
)

type AuditHandler struct {
	auditService *service.AuditService
}

func NewAuditHandler(auditService *service.AuditService) *AuditHandler {
	return &AuditHandler{auditService: auditService}
}

func (h *AuditHandler) AccessLogs(c *gin.Context) {
	var params model.PageParams
	if err := c.ShouldBindQuery(&params); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	if params.Page == 0 {
		params.Page = 1
	}
	if params.Limit == 0 {
		params.Limit = 20
	}

	logs, total, err := h.auditService.AccessLogs(c.Request.Context(), &params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取访问日志失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    logs,
		Meta: &model.Meta{
			Total: total,
			Page:  params.Page,
			Limit: params.Limit,
		},
	})
}

func (h *AuditHandler) TrafficLogs(c *gin.Context) {
	var params model.PageParams
	if err := c.ShouldBindQuery(&params); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	if params.Page == 0 {
		params.Page = 1
	}
	if params.Limit == 0 {
		params.Limit = 20
	}

	logs, total, err := h.auditService.TrafficLogs(c.Request.Context(), &params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取流量日志失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    logs,
		Meta: &model.Meta{
			Total: total,
			Page:  params.Page,
			Limit: params.Limit,
		},
	})
}

func (h *AuditHandler) PolicyChangeLogs(c *gin.Context) {
	var params model.PageParams
	if err := c.ShouldBindQuery(&params); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	if params.Page == 0 {
		params.Page = 1
	}
	if params.Limit == 0 {
		params.Limit = 20
	}

	logs, total, err := h.auditService.PolicyChangeLogs(c.Request.Context(), &params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取策略变更日志失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    logs,
		Meta: &model.Meta{
			Total: total,
			Page:  params.Page,
			Limit: params.Limit,
		},
	})
}

func (h *AuditHandler) OnlineLogs(c *gin.Context) {
	var params model.PageParams
	if err := c.ShouldBindQuery(&params); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	if params.Page == 0 {
		params.Page = 1
	}
	if params.Limit == 0 {
		params.Limit = 20
	}

	logs, total, err := h.auditService.OnlineLogs(c.Request.Context(), &params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取上下线日志失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    logs,
		Meta: &model.Meta{
			Total: total,
			Page:  params.Page,
			Limit: params.Limit,
		},
	})
}

func (h *AuditHandler) Export(c *gin.Context) {
	var params model.PageParams
	if err := c.ShouldBindQuery(&params); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	// 获取导出参数
	logType := c.DefaultQuery("log_type", "admin")
	format := c.DefaultQuery("format", "json")

	opts := &service.ExportOptions{
		LogType: service.ExportLogType(logType),
		Format:  service.ExportFormat(format),
	}

	data, err := h.auditService.Export(c.Request.Context(), &params, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"导出日志失败："+err.Error(),
			nil,
		))
		return
	}

	// 根据格式设置响应头
	switch format {
	case "csv":
		c.Header("Content-Type", "text/csv")
		c.Header("Content-Disposition", "attachment; filename=audit_logs.csv")
		c.Data(http.StatusOK, "text/csv", data)
	default:
		c.JSON(http.StatusOK, model.SuccessResponse{
			Message: "导出成功",
			Data:    string(data),
		})
	}
}

func (h *AuditHandler) AdminLogs(c *gin.Context) {
	var params model.PageParams
	if err := c.ShouldBindQuery(&params); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	if params.Page == 0 {
		params.Page = 1
	}
	if params.Limit == 0 {
		params.Limit = 20
	}

	logs, total, err := h.auditService.AdminLogs(c.Request.Context(), &params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取管理员日志失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    logs,
		Meta: &model.Meta{
			Total: total,
			Page:  params.Page,
			Limit: params.Limit,
		},
	})
}

func (h *AuditHandler) AuditStatus(c *gin.Context) {
	status, err := h.auditService.AuditStatus(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取审计状态失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    status,
	})
}
