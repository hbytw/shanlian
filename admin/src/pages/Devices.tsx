import { useEffect, useState } from 'react'
import { Table, Tag, Space, Button, Input, message, Modal, Select, Form } from 'antd'
import { SearchOutlined, LinkOutlined, DisconnectOutlined, SwapOutlined } from '@ant-design/icons'
import { get, post } from '../services/api'
import type { Device, User, PageParams } from '../types/api'

export function DevicesPage() {
  const [devices, setDevices] = useState<Device[]>([])
  const [loading, setLoading] = useState(false)
  const [params, setParams] = useState<PageParams>({ page: 1, limit: 20 })
  const [users, setUsers] = useState<User[]>([])
  const [transferModalVisible, setTransferModalVisible] = useState(false)
  const [selectedDevice, setSelectedDevice] = useState<Device | null>(null)
  const [transferForm] = Form.useForm()

  const fetchDevices = async () => {
    setLoading(true)
    try {
      const res = await get<Device[]>('/admin/devices', params)
      setDevices(res.data || [])
    } catch (error) {
      message.error('获取设备列表失败')
    } finally {
      setLoading(false)
    }
  }

  const fetchUsers = async () => {
    try {
      const res = await get<{ users: User[] }>('/admin/users', { limit: 100 })
      setUsers(res.data?.users || [])
    } catch (error) {
      console.error('获取用户列表失败')
    }
  }

  useEffect(() => {
    fetchDevices()
    fetchUsers()
  }, [params])

  const handleBind = async (deviceId: string) => {
    try {
      await post('/admin/devices/bind', { device_id: deviceId })
      message.success('绑定成功')
      fetchDevices()
    } catch (error) {
      message.error('绑定失败')
    }
  }

  const handleUnbind = async (deviceId: string) => {
    try {
      await post('/admin/devices/unbind', { device_id: deviceId })
      message.success('解绑成功')
      fetchDevices()
    } catch (error) {
      message.error('解绑失败')
    }
  }

  const handleTransfer = (device: Device) => {
    setSelectedDevice(device)
    transferForm.setFieldsValue({ user_id: device.user_id })
    setTransferModalVisible(true)
  }

  const handleTransferSubmit = async (values: { user_id: string }) => {
    if (!selectedDevice) return
    try {
      await post('/admin/devices/transfer', {
        device_id: selectedDevice.device_id,
        target_user_id: values.user_id,
      })
      message.success('转让成功')
      setTransferModalVisible(false)
      fetchDevices()
    } catch (error) {
      message.error('转让失败')
    }
  }

  const columns = [
    { title: '设备 ID', dataIndex: 'device_id', key: 'device_id', render: (id: string) => <Tag>{id}</Tag> },
    { title: '主机名', dataIndex: 'hostname', key: 'hostname', ellipsis: true },
    { title: '公网 IP', dataIndex: 'public_ip', key: 'public_ip' },
    { title: '分配 IP', dataIndex: 'assigned_ip', key: 'assigned_ip' },
    { title: 'NAT 类型', dataIndex: 'nat_type', key: 'nat_type' },
    {
      title: '在线状态',
      dataIndex: 'online',
      key: 'online',
      render: (online: boolean) => online ? <Tag color="success">在线</Tag> : <Tag color="default">离线</Tag>,
    },
    { title: '系统类型', dataIndex: 'os_type', key: 'os_type' },
    { title: '客户端版本', dataIndex: 'client_version', key: 'client_version' },
    { title: '最后在线', dataIndex: 'last_seen_at', key: 'last_seen_at', render: (t: string) => new Date(t).toLocaleString() },
    {
      title: '操作',
      key: 'action',
      render: (_: unknown, record: Device) => (
        <Space>
          {record.user_id ? (
            <>
              <Button type="link" icon={<SwapOutlined />} onClick={() => handleTransfer(record)}>
                转让
              </Button>
              <Button type="link" danger icon={<DisconnectOutlined />} onClick={() => handleUnbind(record.device_id)}>
                解绑
              </Button>
            </>
          ) : (
            <Button type="link" icon={<LinkOutlined />} onClick={() => handleBind(record.device_id)}>
              绑定
            </Button>
          )}
        </Space>
      ),
    },
  ]

  return (
    <div>
      <div style={{ marginBottom: 16 }}>
        <Input
          placeholder="搜索设备 ID/主机名"
          prefix={<SearchOutlined />}
          style={{ width: 300 }}
          onChange={(e) => setParams({ ...params, search: e.target.value })}
        />
      </div>
      <Table
        columns={columns}
        dataSource={devices}
        rowKey="id"
        loading={loading}
        pagination={{
          current: params.page,
          pageSize: params.limit,
          total: 0,
          onChange: (page, limit) => setParams({ ...params, page, limit }),
        }}
      />

      <Modal
        title="转让设备"
        open={transferModalVisible}
        onCancel={() => setTransferModalVisible(false)}
        onOk={() => transferForm.submit()}
      >
        <Form form={transferForm} layout="vertical" onFinish={handleTransferSubmit}>
          <Form.Item label="当前设备">
            <Tag>{selectedDevice?.device_id}</Tag>
          </Form.Item>
          <Form.Item
            name="user_id"
            label="目标用户"
            rules={[{ required: true, message: '请选择目标用户' }]}
          >
            <Select showSearch placeholder="选择用户" optionFilterProp="label">
              {users.map((user) => (
                <Select.Option key={user.id} value={user.id} label={user.username}>
                  {user.username} ({user.email})
                </Select.Option>
              ))}
            </Select>
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}
