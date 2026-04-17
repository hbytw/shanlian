package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/service"
)

type NetworkHandler struct {
	networkService *service.NetworkService
}

func NewNetworkHandler(networkService *service.NetworkService) *NetworkHandler {
	return &NetworkHandler{networkService: networkService}
}

func (h *NetworkHandler) Create(c *gin.Context) {
	var req model.NetworkCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	network, err := h.networkService.Create(c.Request.Context(), &req, operatorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"创建网络失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusCreated, model.SuccessResponse{
		Message: "创建成功",
		Data:    network,
	})
}

// Allocate 自动分配网段
func (h *NetworkHandler) Allocate(c *gin.Context) {
	var req struct {
		Name string `json:"name" binding:"required"`
		Size int    `json:"size" binding:"required,min=2,max=65534"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	network, err := h.networkService.AllocateCIDR(c.Request.Context(), req.Name, req.Size, operatorID)
	if err != nil {
		status := http.StatusInternalServerError
		if err == service.ErrNetworkNoAvailableIP {
			status = http.StatusConflict
		}
		c.JSON(status, model.NewErrorResponse(
			model.ErrNetworkCIDRConflict,
			"分配网段失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusCreated, model.SuccessResponse{
		Message: "网段分配成功",
		Data:    network,
	})
}

func (h *NetworkHandler) Get(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的网络 ID",
			nil,
		))
		return
	}

	network, err := h.networkService.Get(c.Request.Context(), id)
	if err != nil {
		if err == service.ErrNetworkNotFound {
			c.JSON(http.StatusNotFound, model.NewErrorResponse(
				model.ErrNetworkNotFound,
				"网络不存在",
				nil,
			))
		} else {
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"获取网络失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    network,
	})
}

func (h *NetworkHandler) List(c *gin.Context) {
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

	networks, total, err := h.networkService.List(c.Request.Context(), &params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取网络列表失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    networks,
		Meta: &model.Meta{
			Total: total,
			Page:  params.Page,
			Limit: params.Limit,
		},
	})
}

func (h *NetworkHandler) Update(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的网络 ID",
			nil,
		))
		return
	}

	var req model.NetworkUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.networkService.Update(c.Request.Context(), id, &req, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"更新网络失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "更新成功",
	})
}

func (h *NetworkHandler) Delete(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的网络 ID",
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.networkService.Delete(c.Request.Context(), id, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"删除网络失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "删除成功",
	})
}

func (h *NetworkHandler) ListPeers(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的网络 ID",
			nil,
		))
		return
	}

	peers, err := h.networkService.ListPeers(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取网络节点列表失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    peers,
	})
}

func (h *NetworkHandler) RemovePeer(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的网络 ID",
			nil,
		))
		return
	}

	peerIDStr := c.Param("peer_id")
	peerID, err := uuid.Parse(peerIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的节点 ID",
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.networkService.RemovePeer(c.Request.Context(), id, peerID, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"移除节点失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "移除成功",
	})
}

func (h *NetworkHandler) ListUsers(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的网络 ID",
			nil,
		))
		return
	}

	var params model.PageParams
	if err := c.ShouldBindQuery(&params); err != nil {
		params = model.PageParams{Page: 1, Limit: 20}
	}

	users, total, err := h.networkService.ListUsers(c.Request.Context(), id, &params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取网络用户列表失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    users,
		Meta: &model.Meta{
			Total: total,
			Page:  params.Page,
			Limit: params.Limit,
		},
	})
}

func (h *NetworkHandler) AssignUser(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的网络 ID",
			nil,
		))
		return
	}

	var req model.AssignUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.networkService.AssignUser(c.Request.Context(), id, &req, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"分配用户失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "分配成功",
	})
}

func (h *NetworkHandler) UpdateUserPolicy(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的网络 ID",
			nil,
		))
		return
	}

	userIDStr := c.Param("user_id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的用户 ID",
			nil,
		))
		return
	}

	var req model.UpdateUserPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.networkService.UpdateUserPolicy(c.Request.Context(), id, userID, &req, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"更新用户策略失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "更新成功",
	})
}

func (h *NetworkHandler) RemoveUser(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的网络 ID",
			nil,
		))
		return
	}

	userIDStr := c.Param("user_id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的用户 ID",
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.networkService.RemoveUser(c.Request.Context(), id, userID, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"移除用户失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "移除成功",
	})
}
