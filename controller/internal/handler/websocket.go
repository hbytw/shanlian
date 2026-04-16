package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/hbytw/shanlian/controller/pkg/jwt"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许所有来源，生产环境应限制
	},
}

// WebSocketEventType 事件类型
type WebSocketEventType string

const (
	EventMoonOnline   WebSocketEventType = "moon_online"
	EventMoonOffline  WebSocketEventType = "moon_offline"
	EventPeerOnline   WebSocketEventType = "peer_online"
	EventPeerOffline  WebSocketEventType = "peer_offline"
	EventTunnel       WebSocketEventType = "tunnel_event"
	EventAlert        WebSocketEventType = "alert_event"
	EventTraffic      WebSocketEventType = "traffic_event"
	EventConfigChange WebSocketEventType = "config_change"
)

// WebSocketEvent WebSocket 事件
type WebSocketEvent struct {
	Type      WebSocketEventType `json:"type"`
	Timestamp int64              `json:"timestamp"`
	Data      interface{}         `json:"data"`
}

// ClientConnection 客户端连接
type ClientConnection struct {
	ID       string
	UserID   string
	Conn     *websocket.Conn
	Groups   []string
	JoinTime time.Time
}

// WebSocketHub WebSocket 连接管理器
type WebSocketHub struct {
	clients    map[string]*ClientConnection
	register   chan *ClientConnection
	unregister chan *ClientConnection
	broadcast  chan *WebSocketEvent
	mu         sync.RWMutex
}

func NewWebSocketHub() *WebSocketHub {
	return &WebSocketHub{
		clients:    make(map[string]*ClientConnection),
		register:   make(chan *ClientConnection),
		unregister: make(chan *ClientConnection),
		broadcast:  make(chan *WebSocketEvent, 256),
	}
}

func (h *WebSocketHub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client.ID] = client
			h.mu.Unlock()
			log.Printf("WebSocket 客户端注册：client=%s, user=%s", client.ID, client.UserID)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client.ID]; ok {
				delete(h.clients, client.ID)
				client.Conn.Close()
			}
			h.mu.Unlock()
			log.Printf("WebSocket 客户端注销：client=%s", client.ID)

		case event := <-h.broadcast:
			h.mu.RLock()
			data, _ := json.Marshal(event)
			for _, client := range h.clients {
				if err := client.Conn.WriteMessage(websocket.TextMessage, data); err != nil {
					log.Printf("WebSocket 消息发送失败：client=%s, err=%v", client.ID, err)
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *WebSocketHub) Register(client *ClientConnection) {
	h.register <- client
}

func (h *WebSocketHub) Unregister(client *ClientConnection) {
	h.unregister <- client
}

func (h *WebSocketHub) Broadcast(event *WebSocketEvent) {
	h.broadcast <- event
}

// WebSocketHandler WebSocket 处理器
type WebSocketHandler struct {
	jwtManager *jwt.JWTManager
	hub        *WebSocketHub
}

func NewWebSocketHandler(jwtManager *jwt.JWTManager) *WebSocketHandler {
	hub := NewWebSocketHub()
	go hub.Run()
	return &WebSocketHandler{jwtManager: jwtManager, hub: hub}
}

func (h *WebSocketHandler) HandleWebSocket(c *gin.Context) {
	// 验证 token
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "缺少认证令牌"})
		return
	}

	// 解析 Bearer token
	tokenString := ""
	if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
		tokenString = authHeader[7:]
	}

	claims, err := h.jwtManager.ValidateAccessToken(tokenString)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "无效的认证令牌"})
		return
	}

	// 升级为 WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket 升级失败：%v", err)
		return
	}
	defer conn.Close()

	userID := claims.UserID
	clientID := uuid.New().String()

	// 注册客户端
	client := &ClientConnection{
		ID:       clientID,
		UserID:   userID,
		Conn:     conn,
		Groups:   []string{"admin", "events"},
		JoinTime: time.Now(),
	}
	h.hub.Register(client)

	log.Printf("WebSocket 连接已建立：client=%s, user=%s", clientID, userID)

	// 处理消息循环
	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket 读取错误：%v", err)
			}
			break
		}

		if messageType == websocket.TextMessage {
			log.Printf("收到客户端消息 [%s]: %s", clientID, string(message))
			// 处理客户端消息
			if err := h.handleMessage(client, message); err != nil {
				log.Printf("处理消息失败：%v", err)
			}
		}
	}

	// 注销客户端
	h.hub.Unregister(client)
	log.Printf("WebSocket 连接已关闭：client=%s", clientID)
}

func (h *WebSocketHandler) handleMessage(client *ClientConnection, message []byte) error {
	// 简单的心跳响应
	if string(message) == "ping" {
		return client.Conn.WriteMessage(websocket.TextMessage, []byte("pong"))
	}

	// 广播消息给客户端
	response := map[string]interface{}{
		"type":      "message",
		"client_id": client.ID,
		"content":   string(message),
	}

	return client.Conn.WriteJSON(response)
}

// BroadcastMoonEvent 广播 Moon 上线/下线事件
func (h *WebSocketHandler) BroadcastMoonEvent(moonID uuid.UUID, domain, publicIP string, online bool) {
	eventType := EventMoonOnline
	if !online {
		eventType = EventMoonOffline
	}

	event := &WebSocketEvent{
		Type:      eventType,
		Timestamp: time.Now().Unix(),
		Data: map[string]interface{}{
			"moon_id":   moonID,
			"domain":    domain,
			"public_ip": publicIP,
			"online":    online,
		},
	}
	h.hub.Broadcast(event)
}

// BroadcastPeerEvent 广播节点上线下线事件
func (h *WebSocketHandler) BroadcastPeerEvent(peerID uuid.UUID, deviceID, hostname string, networkID uuid.UUID, online bool) {
	eventType := EventPeerOnline
	if !online {
		eventType = EventPeerOffline
	}

	event := &WebSocketEvent{
		Type:      eventType,
		Timestamp: time.Now().Unix(),
		Data: map[string]interface{}{
			"peer_id":    peerID,
			"device_id":  deviceID,
			"hostname":   hostname,
			"network_id": networkID,
			"online":     online,
		},
	}
	h.hub.Broadcast(event)
}

// BroadcastTunnelEvent 广播隧道事件
func (h *WebSocketHandler) BroadcastTunnelEvent(peerID uuid.UUID, tunnelType, status string) {
	event := &WebSocketEvent{
		Type:      EventTunnel,
		Timestamp: time.Now().Unix(),
		Data: map[string]interface{}{
			"peer_id":    peerID,
			"tunnel_type": tunnelType,
			"status":     status,
		},
	}
	h.hub.Broadcast(event)
}

// BroadcastAlertEvent 广播告警事件
func (h *WebSocketHandler) BroadcastAlertEvent(alertType, message string, severity string) {
	event := &WebSocketEvent{
		Type:      EventAlert,
		Timestamp: time.Now().Unix(),
		Data: map[string]interface{}{
			"alert_type": alertType,
			"message":    message,
			"severity":   severity,
		},
	}
	h.hub.Broadcast(event)
}

// BroadcastConfigChange 广播配置变更事件
func (h *WebSocketHandler) BroadcastConfigChange(resourceType, resourceID, action string) {
	event := &WebSocketEvent{
		Type:      EventConfigChange,
		Timestamp: time.Now().Unix(),
		Data: map[string]interface{}{
			"resource_type": resourceType,
			"resource_id":   resourceID,
			"action":        action,
		},
	}
	h.hub.Broadcast(event)
}
