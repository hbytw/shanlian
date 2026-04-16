package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/service"
)

type PolicyHandler struct {
	policyService *service.PolicyService
}

func NewPolicyHandler(policyService *service.PolicyService) *PolicyHandler {
	return &PolicyHandler{policyService: policyService}
}

func (h *PolicyHandler) Create(c *gin.Context) {
	var req model.PolicyCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	policy, err := h.policyService.Create(c.Request.Context(), &req, operatorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"创建策略失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusCreated, model.SuccessResponse{
		Message: "创建成功",
		Data:    policy,
	})
}

func (h *PolicyHandler) Get(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的策略 ID",
			nil,
		))
		return
	}

	policy, err := h.policyService.Get(c.Request.Context(), id)
	if err != nil {
		if err == service.ErrPolicyNotFound {
			c.JSON(http.StatusNotFound, model.NewErrorResponse(
				model.ErrPolicyNotFound,
				"策略不存在",
				nil,
			))
		} else {
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"获取策略失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    policy,
	})
}

func (h *PolicyHandler) List(c *gin.Context) {
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

	policies, total, err := h.policyService.List(c.Request.Context(), &params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取策略列表失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    policies,
		Meta: &model.Meta{
			Total: total,
			Page:  params.Page,
			Limit: params.Limit,
		},
	})
}

func (h *PolicyHandler) Update(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的策略 ID",
			nil,
		))
		return
	}

	var req model.PolicyUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.policyService.Update(c.Request.Context(), id, &req, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"更新策略失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "更新成功",
	})
}

func (h *PolicyHandler) Delete(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的策略 ID",
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.policyService.Delete(c.Request.Context(), id, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"删除策略失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "删除成功",
	})
}

func (h *PolicyHandler) UpdateStatus(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的策略 ID",
			nil,
		))
		return
	}

	var req model.PolicyStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.policyService.UpdateStatus(c.Request.Context(), id, &req, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"更新策略状态失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "状态更新成功",
	})
}
