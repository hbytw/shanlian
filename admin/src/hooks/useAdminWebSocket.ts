import { useEffect, useRef, useState, useCallback } from 'react'
import { message } from 'antd'

export type WebSocketEventType =
  | 'moon_online'
  | 'moon_offline'
  | 'peer_online'
  | 'peer_offline'
  | 'tunnel_event'
  | 'alert_event'
  | 'traffic_event'
  | 'config_change'

export interface WebSocketEvent<T = unknown> {
  type: WebSocketEventType
  timestamp: number
  data: T
}

interface UseAdminWebSocketOptions {
  onEvent?: (event: WebSocketEvent) => void
  onMoonOnline?: (data: { moon_id: string; domain: string; online: boolean }) => void
  onMoonOffline?: (data: { moon_id: string; domain: string; online: boolean }) => void
  onPeerOnline?: (data: { peer_id: string; device_id: string }) => void
  onPeerOffline?: (data: { peer_id: string; device_id: string }) => void
  onAlert?: (data: { alert_type: string; message: string; severity: string }) => void
  onConfigChange?: (data: { resource_type: string; resource_id: string; action: string }) => void
  onError?: (error: Event) => void
  enabled?: boolean
}

export function useAdminWebSocket(options: UseAdminWebSocketOptions = {}) {
  const {
    onEvent,
    onMoonOnline,
    onMoonOffline,
    onPeerOnline,
    onPeerOffline,
    onAlert,
    onConfigChange,
    onError,
    enabled = true,
  } = options

  const [connected, setConnected] = useState(false)
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const reconnectAttempts = useRef(0)
  const maxReconnectAttempts = 10

  const getToken = () => localStorage.getItem('access_token')

  const connect = useCallback(() => {
    if (!enabled) return

    const token = getToken()
    if (!token) {
      console.warn('No access token, WebSocket connection skipped')
      return
    }

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${protocol}//${window.location.host}/api/v1/ws/admin?token=${token}`

    try {
      const ws = new WebSocket(wsUrl)

      ws.onopen = () => {
        setConnected(true)
        reconnectAttempts.current = 0
        console.log('Admin WebSocket connected')
      }

      ws.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data) as WebSocketEvent
          onEvent?.(data)

          switch (data.type) {
            case 'moon_online':
              onMoonOnline?.(data.data as { moon_id: string; domain: string; online: boolean })
              break
            case 'moon_offline':
              onMoonOffline?.(data.data as { moon_id: string; domain: string; online: boolean })
              break
            case 'peer_online':
              onPeerOnline?.(data.data as { peer_id: string; device_id: string })
              break
            case 'peer_offline':
              onPeerOffline?.(data.data as { peer_id: string; device_id: string })
              break
            case 'alert_event':
              onAlert?.(data.data as { alert_type: string; message: string; severity: string })
              message.warning(`告警: ${(data.data as { message: string }).message}`)
              break
            case 'config_change':
              onConfigChange?.(data.data as { resource_type: string; resource_id: string; action: string })
              break
          }
        } catch {
          console.error('解析 WebSocket 消息失败')
        }
      }

      ws.onerror = (error) => {
        console.error('WebSocket 错误:', error)
        onError?.(error)
      }

      ws.onclose = () => {
        setConnected(false)
        wsRef.current = null

        // 自动重连
        if (reconnectAttempts.current < maxReconnectAttempts) {
          const delay = Math.min(1000 * Math.pow(2, reconnectAttempts.current), 30000)
          reconnectTimerRef.current = setTimeout(() => {
            reconnectAttempts.current++
            connect()
          }, delay)
        }
      }

      wsRef.current = ws
    } catch (error) {
      console.error('创建 WebSocket 失败:', error)
    }
  }, [enabled, onEvent, onMoonOnline, onMoonOffline, onPeerOnline, onPeerOffline, onAlert, onConfigChange, onError])

  const disconnect = useCallback(() => {
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = null
    }
    reconnectAttempts.current = maxReconnectAttempts // 阻止重连
    if (wsRef.current) {
      wsRef.current.close()
      wsRef.current = null
    }
    setConnected(false)
  }, [])

  const sendMessage = useCallback((data: unknown) => {
    if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify(data))
    }
  }, [])

  useEffect(() => {
    if (enabled) {
      connect()
    }
    return () => {
      disconnect()
    }
  }, [enabled, connect, disconnect])

  return {
    connected,
    connect,
    disconnect,
    sendMessage,
  }
}
