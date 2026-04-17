package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/service"
)

type MoonHandler struct {
	moonService *service.MoonService
}

func NewMoonHandler(moonService *service.MoonService) *MoonHandler {
	return &MoonHandler{moonService: moonService}
}

func (h *MoonHandler) Create(c *gin.Context) {
	var req model.MoonCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	moon, err := h.moonService.Create(c.Request.Context(), &req, operatorID)
	if err != nil {
		if err == service.ErrMoonDuplicateDomain {
			c.JSON(http.StatusConflict, model.NewErrorResponse(
				model.ErrMoonDuplicateDomain,
				"Moon 域名已存在",
				nil,
			))
		} else {
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"创建 Moon 节点失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusCreated, model.SuccessResponse{
		Message: "创建成功",
		Data:    moon,
	})
}

func (h *MoonHandler) Get(c *gin.Context) {
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

	moon, err := h.moonService.Get(c.Request.Context(), id)
	if err != nil {
		if err == service.ErrMoonNotFound {
			c.JSON(http.StatusNotFound, model.NewErrorResponse(
				model.ErrMoonNotFound,
				"Moon 节点不存在",
				nil,
			))
		} else {
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"获取 Moon 节点失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    moon,
	})
}

func (h *MoonHandler) List(c *gin.Context) {
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

	moons, total, err := h.moonService.List(c.Request.Context(), &params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取 Moon 节点列表失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    moons,
		Meta: &model.Meta{
			Total: total,
			Page:  params.Page,
			Limit: params.Limit,
		},
	})
}

func (h *MoonHandler) Update(c *gin.Context) {
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

	var req model.MoonUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.moonService.Update(c.Request.Context(), id, &req, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"更新 Moon 节点失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "更新成功",
	})
}

func (h *MoonHandler) Delete(c *gin.Context) {
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

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.moonService.Delete(c.Request.Context(), id, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"删除 Moon 节点失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "删除成功",
	})
}

// Register Moon 节点注册
func (h *MoonHandler) Register(c *gin.Context) {
	var req model.MoonRegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	resp, err := h.moonService.Register(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"Moon 注册失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "注册成功",
		Data:    resp,
	})
}

// Heartbeat Moon 心跳
func (h *MoonHandler) Heartbeat(c *gin.Context) {
	var req model.MoonHeartbeatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	resp, err := h.moonService.Heartbeat(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"心跳更新失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "心跳成功",
		Data:    resp,
	})
}
