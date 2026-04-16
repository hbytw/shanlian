import { useEffect, useRef, useState, useCallback } from 'react'
import { message } from 'antd'

interface MoonStatus {
  id: string
  online: boolean
  connections: number
  traffic_in: number
  traffic_out: number
  last_heartbeat_at: string
}

interface UseMoonWebSocketOptions {
  moonId?: string
  onStatusChange?: (status: MoonStatus) => void
  onError?: (error: Event) => void
}

export function useMoonWebSocket({ moonId, onStatusChange, onError }: UseMoonWebSocketOptions = {}) {
  const [connected, setConnected] = useState(false)
  const [status, setStatus] = useState<MoonStatus | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const connect = useCallback(() => {
    if (!moonId) return

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${protocol}//${window.location.host}/api/v1/ws/moons/${moonId}`

    try {
      const ws = new WebSocket(wsUrl)

      ws.onopen = () => {
        setConnected(true)
        message.success('Moon WebSocket 连接成功')
      }

      ws.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data) as MoonStatus
          setStatus(data)
          onStatusChange?.(data)
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
        reconnectTimerRef.current = setTimeout(() => {
          connect()
        }, 5000)
      }

      wsRef.current = ws
    } catch (error) {
      console.error('创建 WebSocket 失败:', error)
    }
  }, [moonId, onStatusChange, onError])

  const disconnect = useCallback(() => {
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
      reconnectTimerRef.current = null
    }
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
    connect()
    return () => {
      disconnect()
    }
  }, [connect, disconnect])

  return {
    connected,
    status,
    connect,
    disconnect,
    sendMessage,
  }
}
