import { useState, useEffect } from 'react'
import { Modal, Table, Input, Space, Tag, message } from 'antd'
import { SearchOutlined, SwapOutlined, UserOutlined } from '@ant-design/icons'
import { get, post } from '../services/api'
import type { User, Device } from '../types/api'

interface DeviceTransferModalProps {
  deviceId: string | null
  visible: boolean
  onClose: () => void
  onSuccess: () => void
}

export function DeviceTransferModal({ deviceId, visible, onClose, onSuccess }: DeviceTransferModalProps) {
  const [users, setUsers] = useState<User[]>([])
  const [selectedUser, setSelectedUser] = useState<string | null>(null)
  const [device, setDevice] = useState<Device | null>(null)
  const [loading, setLoading] = useState(false)
  const [searchKey, setSearchKey] = useState('')

  useEffect(() => {
    if (visible) {
      fetchUsers()
      if (deviceId) {
        fetchDevice(deviceId)
      }
    }
  }, [visible, deviceId])

  const fetchDevice = async (id: string) => {
    try {
      const res = await get<Device>(`/admin/devices/${id}`)
      setDevice(res.data || null)
    } catch (error) {
      message.error('获取设备信息失败')
    }
  }

  const fetchUsers = async () => {
    try {
      const res = await get<{ users: User[] }>('/admin/users', { limit: 100 })
      setUsers(res.data?.users || [])
    } catch (error) {
      message.error('获取用户列表失败')
    }
  }

  const handleTransfer = async () => {
    if (!deviceId || !selectedUser) {
      message.warning('请选择目标用户')
      return
    }
    setLoading(true)
    try {
      await post('/admin/devices/transfer', { device_id: deviceId, target_user_id: selectedUser })
      message.success('设备转移成功')
      onSuccess()
      onClose()
    } catch (error) {
      message.error('转移失败')
    } finally {
      setLoading(false)
    }
  }

  const filteredUsers = users.filter((u) =>
    u.username.includes(searchKey) || u.email.includes(searchKey)
  )

  const userColumns = [
    {
      title: '用户',
      key: 'user',
      render: (_: unknown, record: User) => (
        <Space>
          <UserOutlined />
          <span>{record.username}</span>
          <Tag>{record.email}</Tag>
        </Space>
      ),
    },
    {
      title: '角色',
      dataIndex: 'role',
      key: 'role',
      render: (role: string) => {
        const map: Record<string, { label: string; color: string }> = {
          super_admin: { label: '超级管理员', color: 'red' },
          network_admin: { label: '网络管理员', color: 'orange' },
          user: { label: '普通用户', color: 'blue' },
        }
        const config = map[role] || { label: role, color: 'default' }
        return <Tag color={config.color}>{config.label}</Tag>
      },
    },
    {
      title: '状态',
      dataIndex: 'enabled',
      key: 'enabled',
      render: (enabled: boolean) => enabled ? <Tag color="success">启用</Tag> : <Tag color="error">禁用</Tag>,
    },
  ]

  return (
    <Modal
      title={
        <Space>
          <SwapOutlined />
          设备转移
        </Space>
      }
      open={visible}
      onCancel={onClose}
      onOk={handleTransfer}
      okText="确认转移"
      confirmLoading={loading}
      width={600}
    >
      {device && (
        <div style={{ marginBottom: 16, padding: 12, background: '#f5f5f5', borderRadius: 4 }}>
          <Space>
            <span>当前设备：</span>
            <Tag>{device.device_id.slice(0, 8)}...</Tag>
            <span>{device.hostname || '未知主机名'}</span>
          </Space>
        </div>
      )}

      <Input
        placeholder="搜索用户"
        prefix={<SearchOutlined />}
        value={searchKey}
        onChange={(e) => setSearchKey(e.target.value)}
        style={{ marginBottom: 16 }}
      />

      <Table
        columns={userColumns}
        dataSource={filteredUsers}
        rowKey="id"
        size="small"
        pagination={{ pageSize: 5 }}
        rowSelection={{
          type: 'radio',
          onChange: (_, selected) => {
            if (selected.length > 0) {
              setSelectedUser(selected[0].id)
            }
          },
        }}
      />
    </Modal>
  )
}
