import { useState, useEffect } from 'react'
import { Drawer, Descriptions, Table, Tag, Space, Card, Statistic, Row, Col, Button, message, Segmented } from 'antd'
import { UserOutlined, DeleteOutlined, SwapOutlined } from '@ant-design/icons'
import { get, post } from '../services/api'
import type { User, Device } from '../types/api'

interface UserDetailDrawerProps {
  userId: string | null
  visible: boolean
  onClose: () => void
}

interface UserStats {
  total_devices: number
  online_devices: number
  total_usage: number
  last_login: string
}

export function UserDetailDrawer({ userId, visible, onClose }: UserDetailDrawerProps) {
  const [user, setUser] = useState<User | null>(null)
  const [devices, setDevices] = useState<Device[]>([])
  const [stats, setStats] = useState<UserStats | null>(null)
  const [, setLoading] = useState(false)
  const [activeTab, setActiveTab] = useState<'info' | 'devices' | 'usage'>('info')

  useEffect(() => {
    if (userId && visible) {
      fetchUserDetail()
    }
  }, [userId, visible])

  const fetchUserDetail = async () => {
    if (!userId) return
    setLoading(true)
    try {
      const [userRes, devicesRes, statsRes] = await Promise.all([
        get<User>(`/admin/users/${userId}`),
        get<Device[]>(`/admin/users/${userId}/devices`),
        get<UserStats>(`/admin/users/${userId}/stats`),
      ])
      setUser(userRes.data || null)
      setDevices(devicesRes.data || [])
      setStats(statsRes.data || null)
    } catch (error) {
      message.error('获取用户详情失败')
    } finally {
      setLoading(false)
    }
  }

  const handleUnbindDevice = async (deviceId: string) => {
    try {
      await post(`/admin/devices/unbind`, { device_id: deviceId })
      message.success('解绑成功')
      fetchUserDetail()
    } catch (error) {
      message.error('解绑失败')
    }
  }

  const handleTransferDevice = async (deviceId: string, targetUserId: string) => {
    try {
      await post(`/admin/devices/transfer`, { device_id: deviceId, target_user_id: targetUserId })
      message.success('转移成功')
      fetchUserDetail()
    } catch (error) {
      message.error('转移失败')
    }
  }

  const formatBytes = (bytes: number) => {
    if (bytes === 0) return '0 B'
    const k = 1024
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
    const i = Math.floor(Math.log(bytes) / Math.log(k))
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
  }

  const deviceColumns = [
    { title: '设备 ID', dataIndex: 'device_id', key: 'device_id', render: (id: string) => <Tag>{id.slice(0, 8)}...</Tag> },
    { title: '主机名', dataIndex: 'hostname', key: 'hostname' },
    { title: '分配 IP', dataIndex: 'assigned_ip', key: 'assigned_ip' },
    {
      title: '在线状态',
      dataIndex: 'online',
      key: 'online',
      render: (online: boolean) => online ? <Tag color="success">在线</Tag> : <Tag color="default">离线</Tag>,
    },
    { title: '系统', dataIndex: 'os_type', key: 'os_type' },
    { title: '最后在线', dataIndex: 'last_seen_at', key: 'last_seen_at', render: (t: string) => new Date(t).toLocaleString() },
    {
      title: '操作',
      key: 'action',
      render: (_: unknown, record: Device) => (
        <Space>
          <Button type="link" size="small" icon={<SwapOutlined />} onClick={() => handleTransferDevice(record.id, '')}>
            转移
          </Button>
          <Button type="link" size="small" danger icon={<DeleteOutlined />} onClick={() => handleUnbindDevice(record.id)}>
            解绑
          </Button>
        </Space>
      ),
    },
  ]

  if (!user) return null

  const roleLabels: Record<string, string> = {
    super_admin: '超级管理员',
    network_admin: '网络管理员',
    user: '普通用户',
  }

  const roleColors: Record<string, string> = {
    super_admin: 'red',
    network_admin: 'orange',
    user: 'blue',
  }

  return (
    <Drawer
      title={
        <Space>
          <UserOutlined />
          用户详情
        </Space>
      }
      placement="right"
      width={640}
      open={visible}
      onClose={onClose}
    >
      <Segmented
        value={activeTab}
        onChange={(value) => setActiveTab(value as 'info' | 'devices' | 'usage')}
        options={[
          { label: '基本信息', value: 'info' },
          { label: '关联设备', value: 'devices' },
          { label: '使用统计', value: 'usage' },
        ]}
        style={{ marginBottom: 16 }}
        block
      />

      {activeTab === 'info' && (
        <>
          <Descriptions column={2} bordered size="small">
            <Descriptions.Item label="用户名">{user.username}</Descriptions.Item>
            <Descriptions.Item label="邮箱">{user.email}</Descriptions.Item>
            <Descriptions.Item label="手机号">{user.phone || '-'}</Descriptions.Item>
            <Descriptions.Item label="角色">
              <Tag color={roleColors[user.role]}>{roleLabels[user.role]}</Tag>
            </Descriptions.Item>
            <Descriptions.Item label="状态">
              <Tag color={user.enabled ? 'success' : 'error'}>
                {user.enabled ? '启用' : '禁用'}
              </Tag>
            </Descriptions.Item>
            <Descriptions.Item label="创建时间">
              {new Date(user.created_at).toLocaleString()}
            </Descriptions.Item>
          </Descriptions>
        </>
      )}

      {activeTab === 'devices' && (
        <>
          <Row gutter={16} style={{ marginBottom: 16 }}>
            <Col span={8}>
              <Card size="small">
                <Statistic title="设备总数" value={stats?.total_devices || 0} />
              </Card>
            </Col>
            <Col span={8}>
              <Card size="small">
                <Statistic title="在线设备" value={stats?.online_devices || 0} valueStyle={{ color: '#52c41a' }} />
              </Card>
            </Col>
            <Col span={8}>
              <Card size="small">
                <Statistic title="最后登录" value={stats?.last_login ? new Date(stats.last_login).toLocaleDateString() : '-'} />
              </Card>
            </Col>
          </Row>
          <Table
            columns={deviceColumns}
            dataSource={devices}
            rowKey="id"
            size="small"
            pagination={false}
          />
        </>
      )}

      {activeTab === 'usage' && (
        <Row gutter={16}>
          <Col span={12}>
            <Card title="流量使用">
              <Statistic title="累计流量" value={formatBytes(stats?.total_usage || 0)} />
            </Card>
          </Col>
          <Col span={12}>
            <Card title="在线时长">
              <Statistic title="统计周期" value="30 天" />
            </Card>
          </Col>
        </Row>
      )}
    </Drawer>
  )
}
