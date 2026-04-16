import { useEffect, useState } from 'react'
import { Card, Row, Col, Statistic, Typography, Button } from 'antd'
import { ArrowUpOutlined, ArrowDownOutlined, LineChartOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { get } from '../services/api'
import type { StatsRealtime, StatsOverview } from '../types/api'

const { Title } = Typography

export function StatsPage() {
  const navigate = useNavigate()
  const [realtime, setRealtime] = useState<StatsRealtime | null>(null)
  const [overview, setOverview] = useState<StatsOverview | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    const fetchData = async () => {
      try {
        const [realtimeRes, overviewRes] = await Promise.all([
          get<StatsRealtime>('/admin/stats/realtime'),
          get<StatsOverview>('/admin/stats/overview'),
        ])
        setRealtime(realtimeRes.data || null)
        setOverview(overviewRes.data || null)
      } catch (error) {
        console.error('获取统计数据失败：', error)
      } finally {
        setLoading(false)
      }
    }
    fetchData()
  }, [])

  const formatBytes = (bytes: number) => {
    if (bytes === 0) return '0 B'
    const k = 1024
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 24 }}>
        <Title level={3} style={{ margin: 0 }}>监控统计</Title>
        <Button type="primary" icon={<LineChartOutlined />} onClick={() => navigate('/stats/traffic')}>
          流量图表
        </Button>
      </div>

      <Row gutter={16} style={{ marginBottom: 24 }}>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="在线节点总数"
              value={realtime?.online_peers || 0}
              suffix="个"
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="在线设备数"
              value={realtime?.online_devices || 0}
              suffix="个"
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="在线 Moon 节点"
              value={realtime?.online_moons || 0}
              suffix="个"
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="网络总数"
              value={realtime?.total_networks || 0}
              suffix="个"
            />
          </Card>
        </Col>
      </Row>

      <Row gutter={16} style={{ marginBottom: 24 }}>
        <Col span={12}>
          <Card title="实时流量" loading={loading}>
            <Statistic
              title="入站流量"
              value={formatBytes(realtime?.traffic_in || 0)}
              prefix={<ArrowUpOutlined style={{ color: '#52c41a' }} />}
            />
            <Statistic
              title="出站流量"
              value={formatBytes(realtime?.traffic_out || 0)}
              prefix={<ArrowDownOutlined style={{ color: '#ff4d4f' }} />}
              style={{ marginTop: 16 }}
            />
          </Card>
        </Col>
        <Col span={12}>
          <Card title="概览统计" loading={loading}>
            <Statistic title="总用户数" value={overview?.total_users || 0} />
            <Statistic title="总设备数" value={overview?.total_devices || 0} style={{ marginTop: 16 }} />
            <Statistic title="总节点数" value={overview?.total_peers || 0} style={{ marginTop: 16 }} />
          </Card>
        </Col>
      </Row>

      <Row gutter={16}>
        <Col span={12}>
          <Card title="24 小时流量统计" loading={loading}>
            <Statistic
              title="入站"
              value={formatBytes(overview?.traffic_in_24h || 0)}
              prefix={<ArrowUpOutlined style={{ color: '#52c41a' }} />}
            />
            <Statistic
              title="出站"
              value={formatBytes(overview?.traffic_out_24h || 0)}
              prefix={<ArrowDownOutlined style={{ color: '#ff4d4f' }} />}
              style={{ marginTop: 16 }}
            />
          </Card>
        </Col>
        <Col span={12}>
          <Card title="会话统计" loading={loading}>
            <Statistic title="活跃会话数" value={overview?.active_sessions || 0} />
            <Statistic title="24 小时在线峰值" value={overview?.online_peers_24h || 0} suffix="个" style={{ marginTop: 16 }} />
          </Card>
        </Col>
      </Row>
    </div>
  )
}
