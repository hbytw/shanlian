import { useEffect, useState } from 'react'
import { Card, Row, Col, Statistic, Tag, Button, Table, Typography, Descriptions, message, Progress } from 'antd'
import { useParams, useNavigate } from 'react-router-dom'
import { ArrowLeftOutlined, GlobalOutlined, TeamOutlined, ThunderboltOutlined, ReloadOutlined } from '@ant-design/icons'
import { get } from '../services/api'
import type { Network, Peer } from '../types/api'

const { Title, Text } = Typography

interface NetworkStats {
  total_peers: number
  online_peers: number
  total_traffic_in: number
  total_traffic_out: number
  ip_usage: number
}

export function NetworksDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [network, setNetwork] = useState<Network | null>(null)
  const [peers, setPeers] = useState<Peer[]>([])
  const [stats, setStats] = useState<NetworkStats | null>(null)
  const [loading, setLoading] = useState(true)

  const fetchNetworkDetail = async () => {
    if (!id) return
    setLoading(true)
    try {
      const [networkRes, peersRes] = await Promise.all([
        get<Network>(`/admin/networks/${id}`),
        get<Peer[]>(`/admin/networks/${id}/peers`),
      ])
      setNetwork(networkRes.data || null)
      setPeers(peersRes.data || [])
      // 计算简单的统计数据
      const onlinePeers = (peersRes.data || []).filter((p: Peer) => p.online).length
      setStats({
        total_peers: (peersRes.data || []).length,
        online_peers: onlinePeers,
        total_traffic_in: 0,
        total_traffic_out: 0,
        ip_usage: Math.round(((peersRes.data || []).length / 254) * 100),
      })
    } catch (error) {
      message.error('获取网络详情失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchNetworkDetail()
  }, [id])

  const formatBytes = (bytes: number) => {
    if (bytes === 0) return '0 B'
    const k = 1024
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
  }

  const peerColumns = [
    { title: '节点 ID', dataIndex: 'id', key: 'id', render: (id: string) => <Tag>{id.slice(0, 8)}...</Tag> },
    { title: '分配 IP', dataIndex: 'assigned_ip', key: 'assigned_ip' },
    { title: '公网 IP', dataIndex: 'public_ip', key: 'public_ip', render: (ip: string) => ip || '-' },
    { title: 'NAT 类型', dataIndex: 'nat_type', key: 'nat_type', render: (nat: string) => nat || '-' },
    {
      title: '在线状态',
      dataIndex: 'online',
      key: 'online',
      render: (online: boolean) => online ? <Tag color="success">在线</Tag> : <Tag color="default">离线</Tag>,
    },
    {
      title: '隧道类型',
      dataIndex: 'tunnel_type',
      key: 'tunnel_type',
      render: (type: string) => <Tag>{type || 'UDP'}</Tag>,
    },
  ]

  if (!network) return null

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', marginBottom: 24 }}>
        <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => navigate('/networks')} style={{ marginRight: 16 }}>
          返回
        </Button>
        <Title level={3} style={{ margin: 0 }}>
          <GlobalOutlined /> {network.name}
        </Title>
        <Tag color={network.enabled ? 'success' : 'error'} style={{ marginLeft: 16 }}>
          {network.enabled ? '启用' : '禁用'}
        </Tag>
      </div>

      <Row gutter={16} style={{ marginBottom: 24 }}>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="节点总数"
              value={stats?.total_peers || 0}
              prefix={<TeamOutlined />}
              suffix="个"
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="在线节点"
              value={stats?.online_peers || 0}
              valueStyle={{ color: '#52c41a' }}
              suffix="个"
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="入站流量"
              value={formatBytes(stats?.total_traffic_in || 0)}
              prefix={<ThunderboltOutlined style={{ color: '#52c41a' }} />}
            />
          </Card>
        </Col>
        <Col span={6}>
          <Card loading={loading}>
            <Statistic
              title="出站流量"
              value={formatBytes(stats?.total_traffic_out || 0)}
              prefix={<ThunderboltOutlined style={{ color: '#ff4d4f' }} />}
            />
          </Card>
        </Col>
      </Row>

      <Row gutter={16} style={{ marginBottom: 24 }}>
        <Col span={16}>
          <Card title="网络信息" extra={<Button icon={<ReloadOutlined />} onClick={fetchNetworkDetail}>刷新</Button>}>
            <Descriptions column={2}>
              <Descriptions.Item label="网络名称">{network.name}</Descriptions.Item>
              <Descriptions.Item label="网段">
                <Tag icon={<GlobalOutlined />}>{network.cidr}</Tag>
              </Descriptions.Item>
              <Descriptions.Item label="网关 IP">{network.gateway_ip}</Descriptions.Item>
              <Descriptions.Item label="描述">{network.description || '-'}</Descriptions.Item>
              <Descriptions.Item label="创建时间">{new Date(network.created_at).toLocaleString()}</Descriptions.Item>
            </Descriptions>
          </Card>
        </Col>
        <Col span={8}>
          <Card title="IP 使用情况" loading={loading}>
            <Progress
              percent={stats?.ip_usage || 0}
              status="active"
              format={(percent) => `${percent}%`}
            />
            <Text type="secondary" style={{ display: 'block', marginTop: 8 }}>
              已分配 {stats?.total_peers || 0} / 254 个 IP 地址
            </Text>
          </Card>
        </Col>
      </Row>

      <Card title="节点列表">
        <Table
          columns={peerColumns}
          dataSource={peers}
          rowKey="id"
          pagination={{ pageSize: 10 }}
        />
      </Card>
    </div>
  )
}
