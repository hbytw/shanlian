package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/service"
)

type AuthHandler struct {
	authService *service.AuthService
}

func NewAuthHandler(authService *service.AuthService) *AuthHandler {
	return &AuthHandler{authService: authService}
}

// Register 注册
func (h *AuthHandler) Register(c *gin.Context) {
	var req model.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	user, err := h.authService.Register(c.Request.Context(), &req)
	if err != nil {
		switch err {
		case service.ErrUsernameExists:
			c.JSON(http.StatusConflict, model.NewErrorResponse(
				model.ErrUserAlreadyExists,
				"用户名已被注册",
				nil,
			))
		case service.ErrEmailExists:
			c.JSON(http.StatusConflict, model.NewErrorResponse(
				model.ErrUserAlreadyExists,
				"邮箱已被注册",
				nil,
			))
		default:
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"注册失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusCreated, model.SuccessResponse{
		Message: "注册成功",
		Data:    user,
	})
}

// Login 登录
func (h *AuthHandler) Login(c *gin.Context) {
	var req model.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误："+err.Error(),
			nil,
		))
		return
	}

	resp, err := h.authService.Login(c.Request.Context(), &req)
	if err != nil {
		switch err {
		case service.ErrInvalidCredentials:
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthInvalidCredentials,
				"用户名或密码错误",
				nil,
			))
		case service.ErrUserDisabled:
			c.JSON(http.StatusForbidden, model.NewErrorResponse(
				model.ErrAuthAccountDisabled,
				"账号已被禁用",
				nil,
			))
		default:
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"登录失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "登录成功",
		Data:    resp,
	})
}

// Refresh 刷新令牌
func (h *AuthHandler) Refresh(c *gin.Context) {
	var req model.RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	resp, err := h.authService.Refresh(c.Request.Context(), &req)
	if err != nil {
		c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
			model.ErrAuthTokenExpired,
			"刷新令牌无效或已过期",
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "刷新成功",
		Data:    resp,
	})
}

// LoginByPhone 手机号登录
func (h *AuthHandler) LoginByPhone(c *gin.Context) {
	var req struct {
		Phone string `json:"phone" binding:"required"`
		Code  string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	resp, err := h.authService.LoginByPhone(c.Request.Context(), req.Phone, req.Code)
	if err != nil {
		switch err {
		case service.ErrCodeInvalid:
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthCodeInvalid,
				"验证码错误",
				nil,
			))
		case service.ErrUserNotFound:
			c.JSON(http.StatusNotFound, model.NewErrorResponse(
				model.ErrUserNotFound,
				"用户不存在",
				nil,
			))
		case service.ErrUserDisabled:
			c.JSON(http.StatusForbidden, model.NewErrorResponse(
				model.ErrAuthAccountDisabled,
				"账号已被禁用",
				nil,
			))
		default:
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"登录失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "登录成功",
		Data:    resp,
	})
}

// LoginByEmail 邮箱登录
func (h *AuthHandler) LoginByEmail(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
		Code  string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	resp, err := h.authService.LoginByEmail(c.Request.Context(), req.Email, req.Code)
	if err != nil {
		switch err {
		case service.ErrCodeInvalid:
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthCodeInvalid,
				"验证码错误",
				nil,
			))
		case service.ErrUserNotFound:
			c.JSON(http.StatusNotFound, model.NewErrorResponse(
				model.ErrUserNotFound,
				"用户不存在",
				nil,
			))
		case service.ErrUserDisabled:
			c.JSON(http.StatusForbidden, model.NewErrorResponse(
				model.ErrAuthAccountDisabled,
				"账号已被禁用",
				nil,
			))
		default:
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"登录失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "登录成功",
		Data:    resp,
	})
}

// SendCode 发送验证码
func (h *AuthHandler) SendCode(c *gin.Context) {
	var req model.SendCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	// 确定发送目标
	target := ""
	codeType := "email"
	if req.Email != "" {
		target = req.Email
		codeType = "email"
	} else if req.Phone != "" {
		target = req.Phone
		codeType = "phone"
	} else {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请提供邮箱或手机号",
			nil,
		))
		return
	}

	err := h.authService.SendCode(c.Request.Context(), target, codeType)
	if err != nil {
		c.JSON(http.StatusTooManyRequests, model.NewErrorResponse(
			model.ErrAuthRateLimitExceeded,
			err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "验证码已发送",
	})
}

// ResetPassword 重置密码
func (h *AuthHandler) ResetPassword(c *gin.Context) {
	var req model.ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	err := h.authService.ResetPasswordByCode(c.Request.Context(), req.Email, req.Code, req.Password)
	if err != nil {
		switch err {
		case service.ErrCodeInvalid:
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthCodeInvalid,
				"验证码错误",
				nil,
			))
		case service.ErrUserNotFound:
			c.JSON(http.StatusNotFound, model.NewErrorResponse(
				model.ErrUserNotFound,
				"用户不存在",
				nil,
			))
		default:
			c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
				model.ErrSystemInternal,
				"重置密码失败："+err.Error(),
				nil,
			))
		}
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "密码重置成功",
	})
}

// OAuthCallback OAuth 回调（预留）
func (h *AuthHandler) OAuthCallback(c *gin.Context) {
	provider := c.Param("provider")
	var req model.OAuthCallbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	resp, err := h.authService.OAuthLogin(c.Request.Context(), provider, req.Code)
	if err != nil {
		if err == service.ErrOAuthNotSupported {
			c.JSON(http.StatusNotImplemented, model.NewErrorResponse(
				model.ErrSystemInternal,
				"暂不支持该 OAuth 登录方式",
				nil,
			))
			return
		}
		c.JSON(http.StatusInternalServerError, model.NewErrorResponse(
			model.ErrSystemInternal,
			"OAuth 登录失败："+err.Error(),
			nil,
		))
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "OAuth 登录成功",
		Data:    resp,
	})
}

// ClientLogin 客户端登录
func (h *AuthHandler) ClientLogin(c *gin.Context) {
	var req model.ClientLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, model.NewErrorResponse(
			model.ErrSystemValidatationFailed,
			"请求参数错误",
			nil,
		))
		return
	}

	// TODO: 实现客户端登录逻辑
	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "客户端登录成功",
	})
}

// GetClientVersion 获取客户端版本
func (h *AuthHandler) GetClientVersion(c *gin.Context) {
	platform := c.Query("platform")
	version, err := h.authService.GetClientVersion(c.Request.Context(), platform)
	if err != nil || version == nil {
		c.JSON(http.StatusOK, model.SuccessResponse{
			Message: "无可用更新",
			Data:    nil,
		})
		return
	}

	c.JSON(http.StatusOK, model.SuccessResponse{
		Message: "获取成功",
		Data:    version,
	})
}

// UploadVersion 上传版本（预留）
func (h *AuthHandler) UploadVersion(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, model.NewErrorResponse(
		model.ErrSystemInternal,
		"该功能暂未开放",
		nil,
	))
}

// ListVersions 列出版本（预留）
func (h *AuthHandler) ListVersions(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, model.NewErrorResponse(
		model.ErrSystemInternal,
		"该功能暂未开放",
		nil,
	))
}

// DeleteVersion 删除版本（预留）
func (h *AuthHandler) DeleteVersion(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, model.NewErrorResponse(
		model.ErrSystemInternal,
		"该功能暂未开放",
		nil,
	))
}
