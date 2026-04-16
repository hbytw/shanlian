package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/service"
)

type PeerHandler struct {
	peerService *service.PeerService
}

func NewPeerHandler(peerService *service.PeerService) *PeerHandler {
	return &PeerHandler{peerService: peerService}
}

func (h *PeerHandler) Get(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的节点 ID",
			nil,
		))
		return
	}

	peer, err := h.peerService.Get(c.Request.Context(), id)
	if err != nil {
		if err == service.ErrPeerNotFound {
			c.JSON(http.StatusNotFound, model.NewErrorResponse(
				model.ErrPeerNotFound,
				"节点不存在",
				nil,
			))
		} else {
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"获取节点失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    peer,
	})
}

func (h *PeerHandler) List(c *gin.Context) {
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

	peers, total, err := h.peerService.List(c.Request.Context(), &params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取节点列表失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    peers,
		Meta: &model.Meta{
			Total: total,
			Page:  params.Page,
			Limit: params.Limit,
		},
	})
}

func (h *PeerHandler) Disconnect(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的节点 ID",
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.peerService.Disconnect(c.Request.Context(), id, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"断开节点连接失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "断开成功",
	})
}

func (h *PeerHandler) ResetIP(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的节点 ID",
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.peerService.ResetIP(c.Request.Context(), id, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"重置节点 IP 失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "IP 重置成功",
	})
}

// Register 节点注册
func (h *PeerHandler) Register(c *gin.Context) {
	var req model.PeerRegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	peer, err := h.peerService.Register(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"节点注册失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "注册成功",
		Data:    peer,
	})
}

// Heartbeat 节点心跳
func (h *PeerHandler) Heartbeat(c *gin.Context) {
	var req model.PeerHeartbeatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	if err := h.peerService.Heartbeat(c.Request.Context(), req.PeerID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"心跳更新失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "心跳成功",
	})
}

// Discover 节点发现
func (h *PeerHandler) Discover(c *gin.Context) {
	var req model.PeerDiscoverRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	resp, err := h.peerService.Discover(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"节点发现失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "发现成功",
		Data:    resp,
	})
}
