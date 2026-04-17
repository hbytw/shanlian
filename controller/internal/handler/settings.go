package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/service"
)

type SettingsHandler struct {
	settingsService *service.SettingsService
}

func NewSettingsHandler(settingsService *service.SettingsService) *SettingsHandler {
	return &SettingsHandler{settingsService: settingsService}
}

// GetPlatform 获取平台配置
func (h *SettingsHandler) GetPlatform(c *gin.Context) {
	config, err := h.settingsService.GetPlatformConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取平台配置失败："+err.Error(),
			nil,
		))
		return
	}
	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    config,
	})
}

// UpdatePlatform 更新平台配置
func (h *SettingsHandler) UpdatePlatform(c *gin.Context) {
	var req model.PlatformConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	if err := h.settingsService.UpdatePlatformConfig(c.Request.Context(), &req); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"更新平台配置失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "平台配置保存成功",
	})
}

// GetSmtp 获取 SMTP 配置
func (h *SettingsHandler) GetSmtp(c *gin.Context) {
	config, err := h.settingsService.GetSmtpConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取SMTP配置失败："+err.Error(),
			nil,
		))
		return
	}
	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    config,
	})
}

// UpdateSmtp 更新 SMTP 配置
func (h *SettingsHandler) UpdateSmtp(c *gin.Context) {
	var req model.SmtpConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	if err := h.settingsService.UpdateSmtpConfig(c.Request.Context(), &req); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"更新SMTP配置失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "邮件配置保存成功",
	})
}

// TestSmtp 测试 SMTP 配置
func (h *SettingsHandler) TestSmtp(c *gin.Context) {
	var req model.SmtpConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	if err := h.settingsService.TestSmtpConfig(c.Request.Context(), &req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemInternal,
			"测试邮件发送失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "测试邮件发送成功",
	})
}

// GetWebhook 获取 Webhook 配置
func (h *SettingsHandler) GetWebhook(c *gin.Context) {
	config, err := h.settingsService.GetWebhookConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取Webhook配置失败："+err.Error(),
			nil,
		))
		return
	}
	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    config,
	})
}

// UpdateWebhook 更新 Webhook 配置
func (h *SettingsHandler) UpdateWebhook(c *gin.Context) {
	var req model.WebhookConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	if err := h.settingsService.UpdateWebhookConfig(c.Request.Context(), &req); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"更新Webhook配置失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "Webhook配置保存成功",
	})
}

// TestWebhook 测试 Webhook
func (h *SettingsHandler) TestWebhook(c *gin.Context) {
	webhookType := c.Param("type")
	var req struct {
		URL string `json:"url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	if err := h.settingsService.TestWebhook(c.Request.Context(), webhookType, req.URL); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemInternal,
			"测试消息发送失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "测试消息发送成功",
	})
}

// GetCert 获取证书配置
func (h *SettingsHandler) GetCert(c *gin.Context) {
	config, err := h.settingsService.GetCertConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"获取证书配置失败："+err.Error(),
			nil,
		))
		return
	}
	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    config,
	})
}

// UpdateCert 更新证书配置
func (h *SettingsHandler) UpdateCert(c *gin.Context) {
	var req model.CertConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	if err := h.settingsService.UpdateCertConfig(c.Request.Context(), &req); err != nil {
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"更新证书配置失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "证书配置保存成功",
	})
}

// VerifyCert 验证证书
func (h *SettingsHandler) VerifyCert(c *gin.Context) {
	var req model.CertConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	if err := h.settingsService.VerifyCert(c.Request.Context(), &req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemInternal,
			"证书验证失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "证书验证通过",
	})
}
