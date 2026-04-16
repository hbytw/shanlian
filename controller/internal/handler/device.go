package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/service"
)

type DeviceHandler struct {
	deviceService *service.DeviceService
}

func NewDeviceHandler(deviceService *service.DeviceService) *DeviceHandler {
	return &DeviceHandler{deviceService: deviceService}
}

func (h *DeviceHandler) Get(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的设备 ID",
			nil,
		))
		return
	}

	device, err := h.deviceService.Get(c.Request.Context(), id)
	if err != nil {
		if err == service.ErrDeviceNotFound {
			c.JSON(http.StatusNotFound, model.NewErrorResponse(
				model.ErrDeviceNotFound,
				"设备不存在",
				nil,
			))
		} else {
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"获取设备失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    device,
	})
}

func (h *DeviceHandler) List(c *gin.Context) {
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

	devices, total, err := h.deviceService.List(c.Request.Context(), &params)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取设备列表失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    devices,
		Meta: &model.Meta{
			Total: total,
			Page:  params.Page,
			Limit: params.Limit,
		},
	})
}

func (h *DeviceHandler) Update(c *gin.Context) {
	idStr := c.Param("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"无效的设备 ID",
			nil,
		))
		return
	}

	var req model.Device
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.deviceService.Update(c.Request.Context(), id, &req, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"更新设备失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "更新成功",
	})
}

func (h *DeviceHandler) Bind(c *gin.Context) {
	var req model.DeviceBindRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.deviceService.Bind(c.Request.Context(), &req, operatorID); err != nil {
		if err == service.ErrDeviceNotFound {
			c.JSON(http.StatusNotFound, model.NewErrorResponse(
				model.ErrDeviceNotFound,
				"设备不存在",
				nil,
			))
		} else if err == service.ErrDeviceAlreadyBind {
			c.JSON(http.StatusConflict, model.NewErrorResponse(
				model.ErrDeviceAlreadyBound,
				"设备已被绑定",
				nil,
			))
		} else {
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"绑定设备失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "绑定成功",
	})
}

func (h *DeviceHandler) Unbind(c *gin.Context) {
	var req model.DeviceUnbindRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.deviceService.Unbind(c.Request.Context(), &req, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"解绑设备失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "解绑成功",
	})
}

func (h *DeviceHandler) Transfer(c *gin.Context) {
	var req model.DeviceTransferRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	operatorID, _ := uuid.Parse(c.GetString("user_id"))
	if err := h.deviceService.Transfer(c.Request.Context(), &req, operatorID); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"转移设备失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "转移成功",
	})
}
