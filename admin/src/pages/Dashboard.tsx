import { useEffect, useState } from 'react'
import { Card, Row, Col, Statistic, Tag, Typography } from 'antd'
import { ArrowUpOutlined, ArrowDownOutlined } from '@ant-design/icons'
import { get } from '../services/api'
import type { StatsRealtime } from '../types/api'

const { Title } = Typography

export function DashboardPage() {
  const [stats, setStats] = useState<StatsRealtime | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    const fetchStats = async () => {
      try {
        const res = await get<StatsRealtime>('/admin/stats/realtime')
        setStats(res.data || null)
      } catch (error) {
        console.error('获取统计数据失败：', error)
      } finally {
        setLoading(false)
      }
    }
    fetchStats()
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
          <Card title="系统状态" loading={loading}>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
              <Tag color="success">数据库：正常</Tag>
              <Tag color="success">Redis：正常</Tag>
              <Tag color={stats?.online_moons ? 'success' : 'warning'}>
                Moon 节点：{stats?.online_moons || 0} 在线
              </Tag>
            </div>
          </Card>
        </Col>
      </Row>
    </div>
  )
}
