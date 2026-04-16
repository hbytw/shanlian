package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/pkg/jwt"
)

// AuthJWT JWT 认证中间件
func AuthJWT(jwtManager *jwt.JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthMissingToken,
				"缺少访问令牌",
				nil,
			))
			c.Abort()
			return
		}

		// Bearer token
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthTokenInvalid,
				"令牌格式无效",
				nil,
			))
			c.Abort()
			return
		}

		tokenString := parts[1]
		claims, err := jwtManager.ValidateAccessToken(tokenString)
		if err != nil {
			var message string
			switch err {
			case jwt.ErrTokenExpired:
				message = "访问令牌已过期，请重新登录"
			default:
				message = "访问令牌无效"
			}
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthTokenInvalid,
				message,
				nil,
			))
			c.Abort()
			return
		}

		// 将用户信息存入上下文
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("role", claims.Role)

		c.Next()
	}
}

// RequireRole 角色权限中间件
func RequireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userRole, exists := c.Get("role")
		if !exists {
			c.JSON(http.StatusUnauthorized, model.NewErrorResponse(
				model.ErrAuthInsufficientPerms,
				"未认证",
				nil,
			))
			c.Abort()
			return
		}

		role := userRole.(string)
		for _, r := range roles {
			if role == r {
				c.Next()
				return
			}
		}

		c.JSON(http.StatusForbidden, model.NewErrorResponse(
			model.ErrAuthInsufficientPerms,
			"权限不足",
			nil,
		))
		c.Abort()
	}
}
