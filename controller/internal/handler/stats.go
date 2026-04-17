package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/service"
)

type StatsHandler struct {
	statsService *service.StatsService
}

func NewStatsHandler(statsService *service.StatsService) *StatsHandler {
	return &StatsHandler{statsService: statsService}
}

func (h *StatsHandler) Realtime(c *gin.Context) {
	stats, err := h.statsService.Realtime(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取实时统计失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    stats,
	})
}

func (h *StatsHandler) Overview(c *gin.Context) {
	stats, err := h.statsService.Overview(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取概览统计失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    stats,
	})
}

func (h *StatsHandler) Traffic(c *gin.Context) {
	params := &service.TrafficQueryParams{}

	// 解析时间范围参数
	if startStr := c.Query("start_time"); startStr != "" {
		if startTime, err := time.Parse(time.RFC3339, startStr); err == nil {
			params.StartTime = startTime
		}
	}
	if endStr := c.Query("end_time"); endStr != "" {
		if endTime, err := time.Parse(time.RFC3339, endStr); err == nil {
			params.EndTime = endTime
		}
	}

	// 设置默认值
	if params.StartTime.IsZero() {
		params.StartTime = time.Now().Add(-24 * time.Hour)
	}
	if params.EndTime.IsZero() {
		params.EndTime = time.Now()
	}

	params.GroupBy = c.DefaultQuery("group_by", "hour")

	stats, err := h.statsService.Traffic(c.Request.Context(), params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取流量统计失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    stats,
	})
}

func (h *StatsHandler) Top(c *gin.Context) {
	stats, err := h.statsService.Top(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取 Top N 统计失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    stats,
	})
}

func (h *StatsHandler) MoonStats(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的 Moon ID",
			nil,
		))
		return
	}

	stats, err := h.statsService.GetMoonStats(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取 Moon 统计失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    stats,
	})
}
