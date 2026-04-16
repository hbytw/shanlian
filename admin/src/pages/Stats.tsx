import { useEffect, useState } from 'react'
import { Card, Row, Col, Statistic, Typography, Button, Progress, List, Tag, Space } from 'antd'
import { ArrowUpOutlined, ArrowDownOutlined, LineChartOutlined, GlobalOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { get } from '../services/api'
import type { StatsRealtime, StatsOverview, MoonNode } from '../types/api'

const { Title } = Typography

export function StatsPage() {
  const navigate = useNavigate()
  const [realtime, setRealtime] = useState<StatsRealtime | null>(null)
  const [overview, setOverview] = useState<StatsOverview | null>(null)
  const [moons, setMoons] = useState<MoonNode[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    const fetchData = async () => {
      try {
        const [realtimeRes, overviewRes, moonsRes] = await Promise.all([
          get<StatsRealtime>('/admin/stats/realtime'),
          get<StatsOverview>('/admin/stats/overview'),
          get<MoonNode[]>('/admin/moons', { limit: 100 }),
        ])
        setRealtime(realtimeRes.data || null)
        setOverview(overviewRes.data || null)
        setMoons(moonsRes.data || [])
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

  // 计算 Moon 负载分布
  const totalConnections = moons.reduce((sum, m) => sum + (m.connections || 0), 0)
  const moonLoads = moons
    .filter(m => m.online)
    .map(m => ({
      ...m,
      loadPercent: totalConnections > 0 ? Math.round(((m.connections || 0) / totalConnections) * 100) : 0,
    }))
    .sort((a, b) => (b.connections || 0) - (a.connections || 0))

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 24 }}>
        <Title level={3} style={{ margin: 0 }}>监控统计</Title>
        <Space>
          <Button type="primary" icon={<LineChartOutlined />} onClick={() => navigate('/stats/traffic')}>
            流量图表
          </Button>
        </Space>
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

      <Row gutter={16} style={{ marginBottom: 24 }}>
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

      <Row gutter={16}>
        <Col span={24}>
          <Card
            title={
              <Space>
                <GlobalOutlined />
                Moon 负载分布
                <Tag>{moonLoads.length} 个在线</Tag>
              </Space>
            }
            loading={loading}
          >
            {moonLoads.length > 0 ? (
              <List
                size="small"
                dataSource={moonLoads.slice(0, 10)}
                renderItem={(moon) => (
                  <List.Item>
                    <List.Item.Meta
                      avatar={
                        <Progress
                          type="circle"
                          percent={moon.loadPercent}
                          size={40}
                          format={() => moon.connections || 0}
                        />
                      }
                      title={<Space><Tag>{moon.domain}</Tag><Tag color={moon.online ? 'success' : 'default'}>{moon.online ? '在线' : '离线'}</Tag></Space>}
                      description={
                        <Space>
                          <span>入站: <Tag color="green">{formatBytes(0)}</Tag></span>
                          <span>出站: <Tag color="red">{formatBytes(0)}</Tag></span>
                        </Space>
                      }
                    />
                  </List.Item>
                )}
              />
            ) : (
              <div style={{ textAlign: 'center', padding: 24, color: '#999' }}>
                暂无 Moon 节点数据
              </div>
            )}
          </Card>
        </Col>
      </Row>
    </div>
  )
}
