import { useEffect, useState } from 'react'
import { Card, Row, Col, Statistic, Tag, Typography, Timeline, List } from 'antd'
import { ArrowUpOutlined, ArrowDownOutlined, BellOutlined, SyncOutlined } from '@ant-design/icons'
import { get } from '../services/api'
import { useAdminWebSocket } from '../hooks/useAdminWebSocket'
import type { StatsRealtime } from '../types/api'

const { Title } = Typography

interface EventItem {
  id: string
  type: string
  message: string
  time: string
}

export function DashboardPage() {
  const [stats, setStats] = useState<StatsRealtime | null>(null)
  const [loading, setLoading] = useState(true)
  const [events, setEvents] = useState<EventItem[]>([])
  const [refreshing, setRefreshing] = useState(false)

  // WebSocket 实时事件
  const { connected } = useAdminWebSocket({
    onMoonOnline: (data) => {
      setEvents((prev) => [
        { id: Date.now().toString(), type: 'moon_online', message: `Moon ${data.domain} 已上线`, time: new Date().toLocaleTimeString() },
        ...prev.slice(0, 9),
      ])
      refreshStats()
    },
    onMoonOffline: (data) => {
      setEvents((prev) => [
        { id: Date.now().toString(), type: 'moon_offline', message: `Moon ${data.domain} 已离线`, time: new Date().toLocaleTimeString() },
        ...prev.slice(0, 9),
      ])
      refreshStats()
    },
    onPeerOnline: (data) => {
      setEvents((prev) => [
        { id: Date.now().toString(), type: 'peer_online', message: `节点 ${data.peer_id.slice(0, 8)} 上线`, time: new Date().toLocaleTimeString() },
        ...prev.slice(0, 9),
      ])
      refreshStats()
    },
    onPeerOffline: (data) => {
      setEvents((prev) => [
        { id: Date.now().toString(), type: 'peer_offline', message: `节点 ${data.peer_id.slice(0, 8)} 离线`, time: new Date().toLocaleTimeString() },
        ...prev.slice(0, 9),
      ])
      refreshStats()
    },
    onAlert: (data) => {
      setEvents((prev) => [
        { id: Date.now().toString(), type: 'alert', message: `[${data.severity}] ${data.message}`, time: new Date().toLocaleTimeString() },
        ...prev.slice(0, 9),
      ])
    },
  })

  const refreshStats = async () => {
    setRefreshing(true)
    try {
      const res = await get<StatsRealtime>('/admin/stats/realtime')
      setStats(res.data || null)
    } catch (error) {
      console.error('获取统计数据失败：', error)
    } finally {
      setRefreshing(false)
    }
  }

  useEffect(() => {
    refreshStats()
    // 每 30 秒自动刷新
    const timer = setInterval(refreshStats, 30000)
    return () => clearInterval(timer)
  }, [])

  const formatBytes = (bytes: number) => {
    if (bytes === 0) return '0 B'
    const k = 1024
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2) + ' ' + sizes[i])
  }

  return (
    <div>
      <Title level={3}>仪表盘</Title>
      <Row gutter={16} style={{ marginBottom: 24 }}>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="在线节点"
              value={stats?.online_peers || 0}
              valueStyle={{ color: '#3f8600' }}
              suffix="个"
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="在线设备"
              value={stats?.online_devices || 0}
              valueStyle={{ color: '#1890ff' }}
              suffix="个"
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="在线 Moon"
              value={stats?.online_moons || 0}
              valueStyle={{ color: '#722ed1' }}
              suffix="个"
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="总网络数"
              value={stats?.total_networks || 0}
              valueStyle={{ color: '#faad14' }}
              suffix="个"
            />
          </Card>
        </Col>
      </Row>
      <Row gutter={16} style={{ marginBottom: 24 }}>
        <Col span={12}>
          <Card title="流量统计" loading={loading}>
            <Statistic
              title="入站流量"
              value={formatBytes(stats?.traffic_in || 0)}
              prefix={<ArrowUpOutlined style={{ color: '#52c41a' }} />}
            />
            <Statistic
              title="出站流量"
              value={formatBytes(stats?.traffic_out || 0)}
              prefix={<ArrowDownOutlined style={{ color: '#ff4d4f' }} />}
              style={{ marginTop: 16 }}
            />
          </Card>
        </Col>
        <Col span={12}>
          <Card
            title={
              <span>
                实时事件
                <Tag color={connected ? 'success' : 'default'} style={{ marginLeft: 8 }}>
                  {connected ? '已连接' : '未连接'}
                </Tag>
              </span>
            }
            extra={<SyncOutlined spin={refreshing} onClick={refreshStats} />}
            loading={loading}
          >
            <List
              size="small"
              dataSource={events}
              renderItem={(item) => (
                <List.Item>
                  <List.Item.Meta
                    title={item.message}
                    description={item.time}
                  />
                </List.Item>
              )}
              locale={{ emptyText: '暂无事件' }}
            />
          </Card>
        </Col>
      </Row>
      <Row gutter={16}>
        <Col span={24}>
          <Card title="系统状态" loading={loading}>
            <div style={{ display: 'flex', flexDirection: 'row', gap: 16 }}>
              <Tag color="success">数据库：正常</Tag>
              <Tag color="success">Redis：正常</Tag>
              <Tag color={stats?.online_moons ? 'success' : 'warning'}>
                Moon 节点：{stats?.online_moons || 0} 在线
              </Tag>
              <Tag color={connected ? 'success' : 'warning'}>
                WebSocket：{connected ? '已连接' : '未连接'}
              </Tag>
            </div>
          </Card>
        </Col>
      </Row>
    </div>
  )
}
