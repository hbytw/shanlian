import { useEffect, useState } from 'react'
import { Table, Button, Space, Input, Modal, Form, Select, message, Popconfirm, Tag } from 'antd'
import { PlusOutlined, SearchOutlined, EditOutlined, DeleteOutlined, EyeOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { get, post, put, del } from '../services/api'
import type { User, PageParams } from '../types/api'

export function UsersPage() {
  const [users, setUsers] = useState<User[]>([])
  const [loading, setLoading] = useState(false)
  const [modalVisible, setModalVisible] = useState(false)
  const [editingUser, setEditingUser] = useState<User | null>(null)
  const [form] = Form.useForm()
  const [params, setParams] = useState<PageParams>({ page: 1, limit: 20 })
  const navigate = useNavigate()

  const fetchUsers = async () => {
    setLoading(true)
    try {
      const res = await get<User[]>('/admin/users', params)
      setUsers(res.data || [])
    } catch (error) {
      message.error('获取用户列表失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchUsers()
  }, [params])

  const handleAdd = () => {
    setEditingUser(null)
    form.resetFields()
    setModalVisible(true)
  }

  const handleEdit = (user: User) => {
    setEditingUser(user)
    form.setFieldsValue(user)
    setModalVisible(true)
  }

  const handleDelete = async (id: string) => {
    try {
      await del(`/admin/users/${id}`)
      message.success('删除成功')
      fetchUsers()
    } catch (error) {
      message.error('删除失败')
    }
  }

  const handleSubmit = async (values: Record<string, unknown>) => {
    try {
      if (editingUser) {
        await put(`/admin/users/${editingUser.id}`, values)
        message.success('更新成功')
      } else {
        await post('/admin/users', values)
        message.success('创建成功')
      }
      setModalVisible(false)
      fetchUsers()
    } catch (error) {
      message.error(editingUser ? '更新失败' : '创建失败')
    }
  }

  const columns = [
    { title: '用户名', dataIndex: 'username', key: 'username' },
    { title: '邮箱', dataIndex: 'email', key: 'email' },
    { title: '手机号', dataIndex: 'phone', key: 'phone' },
    {
      title: '角色',
      dataIndex: 'role',
      key: 'role',
      render: (role: string) => {
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
      },
    },
    {
      title: '状态',
      dataIndex: 'enabled',
      key: 'enabled',
      render: (enabled: boolean) => (enabled ? <Tag color="success">启用</Tag> : <Tag color="error">禁用</Tag>),
    },
    { title: '创建时间', dataIndex: 'created_at', key: 'created_at', render: (t: string) => new Date(t).toLocaleString() },
    {
      title: '操作',
      key: 'action',
      render: (_: unknown, record: User) => (
        <Space>
          <Button type="link" icon={<EyeOutlined />} onClick={() => navigate(`/users/${record.id}`)}>
            详情
          </Button>
          <Button type="link" icon={<EditOutlined />} onClick={() => handleEdit(record)}>
            编辑
          </Button>
          <Popconfirm title="确认删除？" onConfirm={() => handleDelete(record.id)}>
            <Button type="link" danger icon={<DeleteOutlined />}>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 16 }}>
        <Input
          placeholder="搜索用户名/邮箱"
          prefix={<SearchOutlined />}
          style={{ width: 300 }}
          onChange={(e) => setParams({ ...params, search: e.target.value })}
        />
        <Button type="primary" icon={<PlusOutlined />} onClick={handleAdd}>
          创建用户
        </Button>
      </div>
      <Table columns={columns} dataSource={users} rowKey="id" loading={loading} pagination={{
        current: params.page,
        pageSize: params.limit,
        total: 0,
        onChange: (page, limit) => setParams({ ...params, page, limit }),
      }} />
      <Modal
        title={editingUser ? '编辑用户' : '创建用户'}
        open={modalVisible}
        onCancel={() => setModalVisible(false)}
        onOk={() => form.submit()}
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input />
          </Form.Item>
          <Form.Item name="email" label="邮箱" rules={[{ required: true, type: 'email', message: '请输入正确邮箱' }]}>
            <Input />
          </Form.Item>
          {!editingUser && (
            <Form.Item name="password" label="密码" rules={[{ required: true, min: 8, message: '密码至少8位' }]}>
              <Input.Password />
            </Form.Item>
          )}
          <Form.Item name="role" label="角色" rules={[{ required: true, message: '请选择角色' }]}>
            <Select>
              <Select.Option value="super_admin">超级管理员</Select.Option>
              <Select.Option value="network_admin">网络管理员</Select.Option>
              <Select.Option value="user">普通用户</Select.Option>
            </Select>
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}
