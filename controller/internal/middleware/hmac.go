package middleware

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
	"github.com/hbytw/shanlian/controller/pkg/jwt"
	"github.com/hbytw/shanlian/controller/pkg/nonce"
)

const (
	HeaderAPIKey    = "X-Api-Key"
	HeaderNonce     = "X-Nonce"
	HeaderTimestamp = "X-Timestamp"
	HeaderSignature = "X-Signature"
)

// AuthHMAC HMAC 认证中间件（用于 Moon/Planet 客户端）
func AuthHMAC(jwtManager *jwt.JWTManager, nonceManager *nonce.NonceManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetHeader(HeaderAPIKey)
		nonceStr := c.GetHeader(HeaderNonce)
		timestampStr := c.GetHeader(HeaderTimestamp)
		signature := c.GetHeader(HeaderSignature)

		// 检查必填头
		if apiKey == "" || nonceStr == "" || timestampStr == "" || signature == "" {
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthMissingSignature,
				"缺少认证头",
				map[string]string{
					"required": "X-Api-Key, X-Nonce, X-Timestamp, X-Signature",
				},
			))
			c.Abort()
			return
		}

		// 验证时间戳
		timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
		if err != nil {
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthTimestampExpired,
				"时间戳格式无效",
				nil,
			))
			c.Abort()
			return
		}

		if err := nonceManager.ValidateTimestamp(timestamp); err != nil {
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthTimestampExpired,
				"请求已过期",
				nil,
			))
			c.Abort()
			return
		}

		// 检查 Nonce 是否已使用（防重放）
		if err := nonceManager.Check(c.Request.Context(), nonceStr); err != nil {
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthInvalidSignature,
				"Nonce 已使用（重放攻击？）",
				nil,
			))
			c.Abort()
			return
		}

		// 通过 apiKey 查询 Moon 节点
		moonRepo := repository.NewMoonRepository(nil) // 需要从上下文获取 db
		moon, err := moonRepo.GetByAPIKey(c.Request.Context(), apiKey)
		if err != nil || moon == nil {
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthAPIKeyNotFound,
				"API Key 无效",
				nil,
			))
			c.Abort()
			return
		}

		// 读取请求体用于签名验证
		var bodyBytes []byte
		if c.Request.Body != nil {
			bodyBytes, _ = io.ReadAll(c.Request.Body)
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}

		// 验证签名
		if !nonceManager.ValidateSignature(
			moon.APISecret,
			nonceStr,
			timestamp,
			c.Request.Method,
			c.Request.URL.Path,
			string(bodyBytes),
			signature,
		) {
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthInvalidSignature,
				"签名验证失败",
				nil,
			))
			c.Abort()
			return
		}

		// 添加 Nonce 防止重放
		nonceManager.Add(c.Request.Context(), nonceStr)

		// 将 Moon 信息存入上下文
		c.Set("moon_id", moon.ID.String())
		c.Set("moon_domain", moon.Domain)

		c.Next()
	}
}
