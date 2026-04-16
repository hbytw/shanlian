import { useEffect, useState } from 'react'
import { Card, Descriptions, Table, Tag, Space, Button, Row, Col, Statistic, Typography, message } from 'antd'
import { useParams, useNavigate } from 'react-router-dom'
import { ArrowLeftOutlined, ExclamationCircleOutlined } from '@ant-design/icons'
import { get } from '../services/api'
import type { User, Device, Peer } from '../types/api'

const { Title } = Typography

interface UserStats {
  total_devices: number
  online_devices: number
  total_networks: number
  total_traffic_in: number
  total_traffic_out: number
  created_days: number
}

export function UserDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [user, setUser] = useState<User | null>(null)
  const [devices, setDevices] = useState<Device[]>([])
  const [peers, setPeers] = useState<Peer[]>([])
  const [stats, setStats] = useState<UserStats | null>(null)
  const [loading, setLoading] = useState(true)

  const fetchUserDetail = async () => {
    if (!id) return
    setLoading(true)
    try {
      const [userRes, devicesRes, peersRes, statsRes] = await Promise.all([
        get<User>(`/admin/users/${id}`),
        get<Device[]>(`/admin/users/${id}/devices`),
        get<Peer[]>(`/admin/users/${id}/peers`),
        get<UserStats>(`/admin/users/${id}/stats`),
      ])
      setUser(userRes.data || null)
      setDevices(devicesRes.data || [])
      setPeers(peersRes.data || [])
      setStats(statsRes.data || null)
    } catch (error) {
      message.error('获取用户详情失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchUserDetail()
  }, [id])

  const formatBytes = (bytes: number) => {
    if (bytes === 0) return '0 B'
    const k = 1024
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
  }

  const deviceColumns = [
    { title: '设备 ID', dataIndex: 'device_id', key: 'device_id', render: (id: string) => <Tag>{id}</Tag> },
    { title: '主机名', dataIndex: 'hostname', key: 'hostname', ellipsis: true },
    { title: '分配 IP', dataIndex: 'assigned_ip', key: 'assigned_ip' },
    {
      title: '在线状态',
      dataIndex: 'online',
      key: 'online',
      render: (online: boolean) => online ? <Tag color="success">在线</Tag> : <Tag color="default">离线</Tag>,
    },
    { title: '系统', dataIndex: 'os_type', key: 'os_type' },
    { title: '最后在线', dataIndex: 'last_seen_at', key: 'last_seen_at', render: (t: string) => new Date(t).toLocaleString() },
  ]

  const peerColumns = [
    { title: '网络', dataIndex: 'network_id', key: 'network_id', render: (id: string) => <Tag>{id.slice(0, 8)}...</Tag> },
    { title: '分配 IP', dataIndex: 'assigned_ip', key: 'assigned_ip' },
    { title: '隧道类型', dataIndex: 'tunnel_type', key: 'tunnel_type' },
    {
      title: '在线状态',
      dataIndex: 'online',
      key: 'online',
      render: (online: boolean) => online ? <Tag color="success">在线</Tag> : <Tag color="default">离线</Tag>,
    },
    { title: '公网 IP', dataIndex: 'public_ip', key: 'public_ip' },
    { title: '最后在线', dataIndex: 'last_seen_at', key: 'last_seen_at', render: (t?: string) => t ? new Date(t).toLocaleString() : '-' },
  ]

  const getRoleTag = (role: string) => {
    const colorMap: Record<string, string> = {
      super_admin: 'red',
      network_admin: 'orange',
      user: 'blue',
    }
    const labelMap: Record<string, string> = {
      super_admin: '超级管理员',
      network_admin: '网络管理员',
      user: '普通用户',
    }
    return <Tag color={colorMap[role]}>{labelMap[role] || role}</Tag>
  }

  if (loading) {
    return null
  }

  if (!user) {
    return (
      <Card>
        <Space direction="vertical" style={{ width: '100%', textAlign: 'center' }} size="large">
          <ExclamationCircleOutlined style={{ fontSize: 48, color: '#faad14' }} />
          <Title level={4}>用户不存在</Title>
          <Button type="primary" onClick={() => navigate('/users')}>返回用户列表</Button>
        </Space>
      </Card>
    )
  }

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', marginBottom: 24 }}>
        <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => navigate('/users')} style={{ marginRight: 16 }}>
          返回
        </Button>
        <Title level={3} style={{ margin: 0 }}>用户详情</Title>
      </div>

      <Row gutter={16} style={{ marginBottom: 24 }}>
        <Col span={16}>
          <Card title="基本信息">
            <Descriptions column={2}>
              <Descriptions.Item label="用户名">{user.username}</Descriptions.Item>
              <Descriptions.Item label="邮箱">{user.email}</Descriptions.Item>
              <Descriptions.Item label="手机号">{user.phone || '-'}</Descriptions.Item>
              <Descriptions.Item label="角色">{getRoleTag(user.role)}</Descriptions.Item>
              <Descriptions.Item label="状态">{user.enabled ? <Tag color="success">启用</Tag> : <Tag color="error">禁用</Tag>}</Descriptions.Item>
              <Descriptions.Item label="注册时间">{new Date(user.created_at).toLocaleString()}</Descriptions.Item>
            </Descriptions>
          </Card>
        </Col>
        <Col span={8}>
          <Card title="用户统计">
            <Statistic title="注册天数" value={stats?.created_days || 0} suffix="天" />
            <Statistic title="关联设备" value={stats?.total_devices || 0} style={{ marginTop: 16 }} />
            <Statistic title="在线设备" value={stats?.online_devices || 0} style={{ marginTop: 16 }} />
            <Statistic title="加入网络" value={stats?.total_networks || 0} style={{ marginTop: 16 }} />
          </Card>
        </Col>
      </Row>

      <Row gutter={16} style={{ marginBottom: 24 }}>
        <Col span={12}>
          <Card title="流量统计">
            <Statistic title="总入站流量" value={formatBytes(stats?.total_traffic_in || 0)} />
            <Statistic title="总出站流量" value={formatBytes(stats?.total_traffic_out || 0)} style={{ marginTop: 16 }} />
          </Card>
        </Col>
      </Row>

      <Card title="关联设备" style={{ marginBottom: 24 }}>
        <Table
          columns={deviceColumns}
          dataSource={devices}
          rowKey="id"
          pagination={{ pageSize: 10 }}
        />
      </Card>

      <Card title="网络节点">
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
