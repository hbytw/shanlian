package middleware

import (
	"bytes"
	"io"
	"log"
	"time"

	"github.com/gin-gonic/gin"
)

// Logger 日志中间件
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 开始时间
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		// 读取请求体
		var bodyBytes []byte
		if c.Request.Body != nil {
			bodyBytes, _ = io.ReadAll(c.Request.Body)
			c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}

		// 处理请求
		c.Next()

		// 结束时间
		end := time.Now()
		latency := end.Sub(start)

		// 获取用户信息
		userID, _ := c.Get("user_id")
		username, _ := c.Get("username")

		if raw != "" {
			path = path + "?" + raw
		}

		log.Printf("[GIN] %v | %3d | %13v | %15s | %-7s %s | user=%v(%s)",
			end.Format("2006/01/02 - 15:04:05"),
			c.Writer.Status(),
			latency,
			c.ClientIP(),
			c.Request.Method,
			path,
			username,
			userID,
		)
	}
}
