package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hbytw/shanlian/controller/internal/handler"
	"github.com/hbytw/shanlian/controller/internal/middleware"
	"github.com/hbytw/shanlian/controller/internal/model"
	"github.com/hbytw/shanlian/controller/internal/repository"
	"github.com/hbytw/shanlian/controller/internal/service"
	"github.com/hbytw/shanlian/controller/pkg/event"
	"github.com/hbytw/shanlian/controller/pkg/jwt"
	"github.com/hbytw/shanlian/controller/pkg/nonce"
	"github.com/hbytw/shanlian/controller/pkg/verifycode"
)

// @title 闪连 VPN 管理平台 API
// @version v0.0.0
// @description 闪连 VPN 管理平台后端 API，包含管理平台和 Controller 信令服务
// @host controller.ytw.fr:8443
// @BasePath /api/v1
func main() {
	// ============================================================
	// 初始化配置
	// ============================================================
	cfg := loadConfig()

	// ============================================================
	// 初始化数据库连接
	// ============================================================
	db, err := repository.NewPostgresDB(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("数据库连接失败：%s，请检查 PostgreSQL 服务是否正常运行", err.Error())
	}
	defer db.Close()
	log.Println("数据库连接成功")

	// ============================================================
	// 初始化 Redis 连接
	// ============================================================
	rdb := repository.NewRedisDB(cfg.RedisURL)
	if err := rdb.Ping(context.Background()); err != nil {
		log.Fatalf("Redis 连接失败：%s，请检查 Redis 服务是否正常运行", err.Error())
	}
	defer rdb.Close()
	log.Println("Redis 连接成功")

	// ============================================================
	// 初始化工具包
	// ============================================================
	jwtManager := jwt.NewJWTManager(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	nonceManager := nonce.NewNonceManager(rdb.Client, cfg.NonceTTL)
	verifyCodeManager := verifycode.NewVerifyCodeManager(rdb.Client)

	// ============================================================
	// 初始化仓储层
	// ============================================================
	userRepo := repository.NewUserRepository(db)
	networkRepo := repository.NewNetworkRepository(db)
	deviceRepo := repository.NewDeviceRepository(db)
	moonRepo := repository.NewMoonRepository(db)
	peerRepo := repository.NewPeerRepository(db)
	policyRepo := repository.NewPolicyRepository(db)
	auditRepo := repository.NewAuditRepository(db)
	versionRepo := repository.NewVersionRepository(db.DB)
	settingsRepo := repository.NewSettingsRepository(db.DB)

	// ============================================================
	// 初始化服务层
	// ============================================================
	versionService := service.NewVersionService(versionRepo, auditRepo, os.Getenv("VERSION_UPLOAD_DIR"))
	authService := service.NewAuthService(userRepo, versionRepo, jwtManager, auditRepo, verifyCodeManager)
	userService := service.NewUserService(userRepo, networkRepo, auditRepo)
	networkService := service.NewNetworkService(networkRepo, peerRepo, userRepo, moonRepo, auditRepo)
	deviceService := service.NewDeviceService(deviceRepo, userRepo, networkRepo, auditRepo)
	moonService := service.NewMoonService(moonRepo, auditRepo)
	peerService := service.NewPeerService(peerRepo, deviceRepo, networkRepo, moonRepo, auditRepo)
	policyService := service.NewPolicyService(policyRepo, networkRepo, auditRepo)
	statsService := service.NewStatsService(peerRepo, deviceRepo, moonRepo, networkRepo, auditRepo)
	auditService := service.NewAuditService(auditRepo)
	settingsService := service.NewSettingsService(settingsRepo)

	// ============================================================
	// 初始化处理器层
	// ============================================================
	authHandler := handler.NewAuthHandler(authService)
	userHandler := handler.NewUserHandler(userService)
	networkHandler := handler.NewNetworkHandler(networkService)
	deviceHandler := handler.NewDeviceHandler(deviceService)
	moonHandler := handler.NewMoonHandler(moonService)
	peerHandler := handler.NewPeerHandler(peerService)
	policyHandler := handler.NewPolicyHandler(policyService)
	statsHandler := handler.NewStatsHandler(statsService)
	auditHandler := handler.NewAuditHandler(auditService)
	versionHandler := handler.NewVersionHandler(versionService)
	wsHandler := handler.NewWebSocketHandler(jwtManager)
	settingsHandler := handler.NewSettingsHandler(settingsService)

	// ============================================================
	// 初始化事件订阅
	// ============================================================
	eventBus := event.GetEventBus()
	eventBus.Subscribe(event.Subscriber{
		EventTypes: []event.EventType{event.EventMoonOnline, event.EventMoonOffline},
		Handler: func(e *event.Event) {
			data := e.Data.(map[string]interface{})
			moonID, _ := uuid.Parse(data["moon_id"].(string))
			wsHandler.BroadcastMoonEvent(
				moonID,
				data["domain"].(string),
				data["public_ip"].(string),
				data["online"].(bool),
			)
		},
	})
	eventBus.Subscribe(event.Subscriber{
		EventTypes: []event.EventType{event.EventPeerOnline, event.EventPeerOffline},
		Handler: func(e *event.Event) {
			data := e.Data.(map[string]interface{})
			peerID, _ := uuid.Parse(data["peer_id"].(string))
			networkID, _ := uuid.Parse(data["network_id"].(string))
			wsHandler.BroadcastPeerEvent(
				peerID,
				data["device_id"].(string),
				data["hostname"].(string),
				networkID,
				data["online"].(bool),
			)
		},
	})

	// ============================================================
	// 初始化 Gin 路由器
	// ============================================================
	if cfg.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(middleware.Logger())
	router.Use(middleware.CORS())

	// 健康检查
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":    "healthy",
			"service":   "controller",
			"version":   "v0.0.0",
			"timestamp": time.Now().Unix(),
		})
	})

	// ============================================================
	// API 路由
	// ============================================================
	api := router.Group("/api/v1")
	{
		// 认证接口（公开）
		auth := api.Group("/auth")
		{
			auth.POST("/register", authHandler.Register)
			auth.POST("/login", authHandler.Login)
			auth.POST("/refresh", authHandler.Refresh)
			auth.POST("/login/phone", authHandler.LoginByPhone)
			auth.POST("/login/email", authHandler.LoginByEmail)
			auth.POST("/code/send", authHandler.SendCode)
			auth.POST("/password/reset", authHandler.ResetPassword)
			auth.POST("/oauth/:provider/callback", authHandler.OAuthCallback)
			auth.POST("/client/login", authHandler.ClientLogin)
		}

		// 客户端版本查询（公开）
		api.GET("/version/client", authHandler.GetClientVersion)

		// 需要认证的管理接口
		admin := api.Group("/admin")
		admin.Use(middleware.AuthJWT(jwtManager))
		{
			// 用户管理
			users := admin.Group("/users")
			{
				users.GET("", userHandler.List)
				users.POST("", userHandler.Create)
				users.GET("/:id", userHandler.Get)
				users.PUT("/:id", userHandler.Update)
				users.DELETE("/:id", userHandler.Delete)
				users.POST("/:id/reset-password", userHandler.ResetPassword)
			}

			// 网络管理
			networks := admin.Group("/networks")
			{
				networks.GET("", networkHandler.List)
				networks.POST("", networkHandler.Create)
				networks.POST("/allocate", networkHandler.Allocate)
				networks.GET("/:id", networkHandler.Get)
				networks.PUT("/:id", networkHandler.Update)
				networks.DELETE("/:id", networkHandler.Delete)
				networks.GET("/:id/peers", networkHandler.ListPeers)
				networks.DELETE("/:id/peers/:peer_id", networkHandler.RemovePeer)
				networks.GET("/:id/users", networkHandler.ListUsers)
				networks.POST("/:id/users", networkHandler.AssignUser)
				networks.PUT("/:id/users/:user_id", networkHandler.UpdateUserPolicy)
				networks.DELETE("/:id/users/:user_id", networkHandler.RemoveUser)
			}

			// Moon 管理
			moons := admin.Group("/moons")
			{
				moons.GET("", moonHandler.List)
				moons.POST("", moonHandler.Create)
				moons.GET("/:id", moonHandler.Get)
				moons.PUT("/:id", moonHandler.Update)
				moons.DELETE("/:id", moonHandler.Delete)
				moons.GET("/:id/stats", statsHandler.MoonStats)
			}

			// 设备管理
			devices := admin.Group("/devices")
			{
				devices.GET("", deviceHandler.List)
				devices.GET("/:id", deviceHandler.Get)
				devices.PUT("/:id", deviceHandler.Update)
				devices.POST("/bind", deviceHandler.Bind)
				devices.POST("/unbind", deviceHandler.Unbind)
				devices.POST("/transfer", deviceHandler.Transfer)
			}

			// 节点管理
			peers := admin.Group("/peers")
			{
				peers.GET("", peerHandler.List)
				peers.GET("/:id", peerHandler.Get)
				peers.POST("/:id/disconnect", peerHandler.Disconnect)
				peers.POST("/:id/reset-ip", peerHandler.ResetIP)
			}

			// 策略管理
			policies := admin.Group("/policies")
			{
				policies.GET("", policyHandler.List)
				policies.POST("", policyHandler.Create)
				policies.GET("/:id", policyHandler.Get)
				policies.PUT("/:id", policyHandler.Update)
				policies.DELETE("/:id", policyHandler.Delete)
				policies.PATCH("/:id/status", policyHandler.UpdateStatus)
			}

			// 监控统计
			stats := admin.Group("/stats")
			{
				stats.GET("/realtime", statsHandler.Realtime)
				stats.GET("/overview", statsHandler.Overview)
				stats.GET("/traffic", statsHandler.Traffic)
				stats.GET("/top", statsHandler.Top)
			}

			// 日志审计
			logs := admin.Group("/logs")
			{
				logs.GET("/access", auditHandler.AccessLogs)
				logs.GET("/traffic", auditHandler.TrafficLogs)
				logs.GET("/policy", auditHandler.PolicyChangeLogs)
				logs.GET("/online", auditHandler.OnlineLogs)
				logs.GET("/export", auditHandler.Export)
				logs.GET("/admin", auditHandler.AdminLogs)
			}

			// 审计服务器
			admin.GET("/audit/status", auditHandler.AuditStatus)

			// 版本管理
			versions := admin.Group("/versions")
			{
				versions.POST("", versionHandler.UploadVersion)
				versions.GET("", versionHandler.ListVersions)
				versions.DELETE("/:id", versionHandler.DeleteVersion)
			}

			// 系统设置
			settings := admin.Group("/settings")
			{
				settings.GET("/platform", settingsHandler.GetPlatform)
				settings.PUT("/platform", settingsHandler.UpdatePlatform)
				settings.GET("/smtp", settingsHandler.GetSmtp)
				settings.PUT("/smtp", settingsHandler.UpdateSmtp)
				settings.POST("/smtp/test", settingsHandler.TestSmtp)
				settings.GET("/webhook", settingsHandler.GetWebhook)
				settings.PUT("/webhook", settingsHandler.UpdateWebhook)
				settings.POST("/webhook/:type/test", settingsHandler.TestWebhook)
				settings.GET("/cert", settingsHandler.GetCert)
				settings.PUT("/cert", settingsHandler.UpdateCert)
				settings.POST("/cert/verify", settingsHandler.VerifyCert)
			}
		}

		// ============================================================
		// Moon / Planet 客户端接口（API Key + HMAC 认证）
		// ============================================================
		middleware.SetMoonRepository(moonRepo)
		moonAPI := api.Group("")
		moonAPI.Use(middleware.AuthHMAC(jwtManager, nonceManager))
		{
			// 注意：/moon/register 和 /peer/register 是公开的，不需要认证
			moonAPI.POST("/moon/heartbeat", moonHandler.Heartbeat)
			moonAPI.POST("/peer/heartbeat", peerHandler.Heartbeat)
			moonAPI.POST("/peer/discover", peerHandler.Discover)
			moonAPI.POST("/stats/traffic", func(c *gin.Context) {
				var req model.ReceiveTrafficRequest
				if err := c.ShouldBindJSON(&req); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if err := statsService.ReceiveTraffic(c.Request.Context(), &req); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
				c.JSON(http.StatusOK, gin.H{"message": "流量接收成功"})
			})
		}

		// ============================================================
		// 公开接口（无需认证）
		// ============================================================
		moonPublic := api.Group("")
		{
			moonPublic.POST("/moon/register", moonHandler.Register)
			moonPublic.POST("/peer/register", peerHandler.Register)
		}

		// ============================================================
		// WebSocket（认证用户实时推送）
		// ============================================================
		api.GET("/ws/admin", wsHandler.HandleWebSocket)
	}

	// ============================================================
	// 启动 HTTP 服务器
	// ============================================================
	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 优雅启动
	go func() {
		log.Printf("闪连 Controller 服务启动中，监听端口 %d", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务器启动失败：%s", err.Error())
		}
	}()

	// ============================================================
	// 优雅退出
	// ============================================================
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("收到退出信号，正在关闭服务器...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("服务器强制关闭：%s", err.Error())
	}
	log.Println("服务器已安全关闭")
}

// ============================================================
// 配置结构
// ============================================================
type Config struct {
	Mode          string
	Port          int
	DatabaseURL   string
	RedisURL      string
	JWTSecret     string
	AccessTokenTTL time.Duration
	RefreshTokenTTL time.Duration
	NonceTTL      time.Duration
}

func loadConfig() *Config {
	return &Config{
		Mode:          getEnv("GIN_MODE", "debug"),
		Port:          8443,
		DatabaseURL:   getEnv("DATABASE_URL", "postgres://shanlian:shanlian@localhost:5432/shanlian?sslmode=disable"),
		RedisURL:      getEnv("REDIS_URL", "redis://localhost:6379/0"),
		JWTSecret:     getEnv("JWT_SECRET", "shanlian-jwt-secret-change-in-production"),
		AccessTokenTTL: 24 * time.Hour,
		RefreshTokenTTL: 7 * 24 * time.Hour,
		NonceTTL:      5 * time.Minute,
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
