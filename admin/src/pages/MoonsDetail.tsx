import { useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { Card, Row, Col, Statistic, Tag, Space, Button, Drawer, Descriptions, Timeline, message } from 'antd'
import { ClusterOutlined, HeartOutlined, ThunderboltOutlined, DisconnectOutlined, ReloadOutlined } from '@ant-design/icons'
import { get } from '../services/api'
import { useMoonWebSocket } from '../hooks/useMoonWebSocket'
import type { MoonNode } from '../types/api'

interface MoonDetailDrawerProps {
  moonId: string | null
  visible: boolean
  onClose: () => void
}

interface MoonStats {
  connections: number
  traffic_in: number
  traffic_out: number
  cpu_usage: number
  memory_usage: number
}

const formatBytes = (bytes: number) => {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

export function MoonDetailDrawer({ moonId, visible, onClose }: MoonDetailDrawerProps) {
  const [moon, setMoon] = useState<MoonNode | null>(null)
  const [stats, setStats] = useState<MoonStats | null>(null)
  const [, setLoading] = useState(false)

  const { connected, status: wsStatus } = useMoonWebSocket({
    moonId: moonId || undefined,
    onStatusChange: (data) => {
      setStats({
        connections: data.connections,
        traffic_in: data.traffic_in,
        traffic_out: data.traffic_out,
        cpu_usage: 0,
        memory_usage: 0,
      })
    },
  })

  useEffect(() => {
    if (moonId && visible) {
      fetchMoonDetail()
    }
  }, [moonId, visible])

  const fetchMoonDetail = async () => {
    if (!moonId) return
    setLoading(true)
    try {
      const [moonRes, statsRes] = await Promise.all([
        get<MoonNode>(`/admin/moons/${moonId}`),
        get<MoonStats>(`/admin/moons/${moonId}/stats`),
      ])
      setMoon(moonRes.data || null)
      setStats(statsRes.data || null)
    } catch (error) {
      message.error('获取 Moon 详情失败')
    } finally {
      setLoading(false)
    }
  }

  if (!moon) return null

  return (
    <Drawer
      title={
        <Space>
          <ClusterOutlined />
          Moon 节点详情
          <Tag color={moon.online ? 'success' : 'default'}>
            {moon.online ? '在线' : '离线'}
          </Tag>
        </Space>
      }
      placement="right"
      width={600}
      open={visible}
      onClose={onClose}
      extra={
        <Button icon={<ReloadOutlined />} onClick={fetchMoonDetail}>
          刷新
        </Button>
      }
    >
      <Row gutter={16} style={{ marginBottom: 24 }}>
        <Col span={8}>
          <Card size="small">
            <Statistic
              title="连接数"
              value={wsStatus?.connections || stats?.connections || 0}
              suffix="个"
              valueStyle={{ color: '#1890ff' }}
            />
          </Card>
        </Col>
        <Col span={8}>
          <Card size="small">
            <Statistic
              title="入站流量"
              value={formatBytes(wsStatus?.traffic_in || stats?.traffic_in || 0)}
              prefix={<ThunderboltOutlined style={{ color: '#52c41a' }} />}
            />
          </Card>
        </Col>
        <Col span={8}>
          <Card size="small">
            <Statistic
              title="出站流量"
              value={formatBytes(wsStatus?.traffic_out || stats?.traffic_out || 0)}
              prefix={<ThunderboltOutlined style={{ color: '#ff4d4f' }} />}
            />
          </Card>
        </Col>
      </Row>

      <Descriptions column={2} bordered size="small" style={{ marginBottom: 24 }}>
        <Descriptions.Item label="域名">{moon.domain}</Descriptions.Item>
        <Descriptions.Item label="权重">{moon.weight}</Descriptions.Item>
        <Descriptions.Item label="公网 IP">{moon.public_ip}</Descriptions.Item>
        <Descriptions.Item label="网关 IP">{moon.gateway_ip}</Descriptions.Item>
        <Descriptions.Item label="版本">{moon.version || '-'}</Descriptions.Item>
        <Descriptions.Item label="最后心跳">
          {moon.last_heartbeat_at
            ? new Date(moon.last_heartbeat_at).toLocaleString()
            : '-'}
        </Descriptions.Item>
      </Descriptions>

      <Card title="端口配置" size="small" style={{ marginBottom: 24 }}>
        <Row gutter={16}>
          <Col span={6}>
            <Tag>中继: {moon.relay_port}</Tag>
          </Col>
          <Col span={6}>
            <Tag>WSS: {moon.wss_port}</Tag>
          </Col>
          <Col span={6}>
            <Tag>UDP: {moon.udp_port}</Tag>
          </Col>
          <Col span={6}>
            <Tag>健康检查: {moon.health_port}</Tag>
          </Col>
        </Row>
      </Card>

      <Card
        title={
          <Space>
            <HeartOutlined />
            实时状态
            <Tag color={connected ? 'success' : 'default'}>
              {connected ? '已连接' : '未连接'}
            </Tag>
          </Space>
        }
        size="small"
      >
        {connected ? (
          <Timeline
            items={[
              { color: 'green', children: `连接数: ${wsStatus?.connections || 0}` },
              { color: 'blue', children: `入站: ${formatBytes(wsStatus?.traffic_in || 0)}` },
              { color: 'red', children: `出站: ${formatBytes(wsStatus?.traffic_out || 0)}` },
              { color: 'gray', children: `心跳: ${wsStatus?.last_heartbeat_at ? new Date(wsStatus.last_heartbeat_at).toLocaleString() : '-'}` },
            ]}
          />
        ) : (
          <Space direction="vertical" style={{ width: '100%' }}>
            <Tag icon={<DisconnectOutlined />} color="default">
              WebSocket 未连接
            </Tag>
            <Button onClick={fetchMoonDetail}>重新获取状态</Button>
          </Space>
        )}
      </Card>
    </Drawer>
  )
}

export function MoonsDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  return (
    <MoonDetailDrawer
      moonId={id || null}
      visible={true}
      onClose={() => navigate('/moons')}
    />
  )
}
