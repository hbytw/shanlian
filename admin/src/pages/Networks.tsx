import { useEffect, useState } from 'react'
import { Table, Button, Space, Input, Modal, Form, message, Popconfirm, Tag, Switch, InputNumber, Alert } from 'antd'
import { PlusOutlined, SearchOutlined, EditOutlined, DeleteOutlined, ClusterOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { get, post, put, del } from '../services/api'
import type { Network, PageParams } from '../types/api'

export function NetworksPage() {
  const [networks, setNetworks] = useState<Network[]>([])
  const [loading, setLoading] = useState(false)
  const [modalVisible, setModalVisible] = useState(false)
  const [editingNetwork, setEditingNetwork] = useState<Network | null>(null)
  const [form] = Form.useForm()
  const [params, setParams] = useState<PageParams>({ page: 1, limit: 20 })
  const [autoAllocate, setAutoAllocate] = useState(true)
  const [requiredSize, setRequiredSize] = useState(254)

  const fetchNetworks = async () => {
    setLoading(true)
    try {
      const res = await get<Network[]>('/admin/networks', params)
      setNetworks(res.data || [])
    } catch (error) {
      message.error('获取网络列表失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchNetworks()
  }, [params])

  const handleAdd = () => {
    setEditingNetwork(null)
    form.resetFields()
    setAutoAllocate(true)
    setModalVisible(true)
  }

  const handleEdit = (network: Network) => {
    setEditingNetwork(network)
    form.setFieldsValue(network)
    setModalVisible(true)
  }

  const handleDelete = async (id: string) => {
    try {
      await del(`/admin/networks/${id}`)
      message.success('删除成功')
      fetchNetworks()
    } catch (error) {
      message.error('删除失败')
    }
  }

  const handleSubmit = async (values: Record<string, unknown>) => {
    try {
      if (editingNetwork) {
        await put(`/admin/networks/${editingNetwork.id}`, values)
        message.success('更新成功')
      } else {
        if (autoAllocate) {
          // 调用自动分配 API
          const res = await post<Network>('/admin/networks/allocate', {
            name: values.name,
            size: requiredSize,
          })
          if (res.data) {
            message.success(`创建成功，网段：${res.data.cidr}，网关：${res.data.gateway_ip}`)
          }
        } else {
          await post('/admin/networks', values)
          message.success('创建成功')
        }
      }
      setModalVisible(false)
      fetchNetworks()
    } catch (error) {
      message.error(editingNetwork ? '更新失败' : '创建失败')
    }
  }

  const columns = [
    { title: '网络名称', dataIndex: 'name', key: 'name' },
    { title: '网段', dataIndex: 'cidr', key: 'cidr', render: (cidr: string) => <Tag icon={<ClusterOutlined />}>{cidr}</Tag> },
    { title: '网关 IP', dataIndex: 'gateway_ip', key: 'gateway_ip' },
    { title: '描述', dataIndex: 'description', key: 'description', ellipsis: true },
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
      render: (_: unknown, record: Network) => (
        <Space>
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
          placeholder="搜索网络名称"
          prefix={<SearchOutlined />}
          style={{ width: 300 }}
          onChange={(e) => setParams({ ...params, search: e.target.value })}
        />
        <Button type="primary" icon={<PlusOutlined />} onClick={handleAdd}>
          创建网络
        </Button>
      </div>
      <Table columns={columns} dataSource={networks} rowKey="id" loading={loading} pagination={{
        current: params.page,
        pageSize: params.limit,
        total: 0,
        onChange: (page, limit) => setParams({ ...params, page, limit }),
      }} />
      <Modal
        title={editingNetwork ? '编辑网络' : '创建网络'}
        open={modalVisible}
        onCancel={() => setModalVisible(false)}
        onOk={() => form.submit()}
        width={600}
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit}>
          <Form.Item name="name" label="网络名称" rules={[{ required: true, message: '请输入网络名称' }]}>
            <Input placeholder="例如：办公网络" />
          </Form.Item>
          {!editingNetwork && (
            <>
              <Form.Item label="分配方式">
                <Space>
                  <Switch checked={autoAllocate} onChange={setAutoAllocate} />
                  <span>{autoAllocate ? '自动分配网段' : '手动指定网段'}</span>
                </Space>
              </Form.Item>
              {autoAllocate ? (
                <Alert
                  message="自动分配将根据您指定的 IP 数量自动分配可用网段"
                  type="info"
                  showIcon
                  style={{ marginBottom: 16 }}
                />
              ) : (
                <>
                  <Form.Item
                    name="cidr"
                    label="网段"
                    rules={[{ required: !autoAllocate, message: '请输入网段' }]}
                    extra="示例：10.8.0.0/16"
                  >
                    <Input placeholder="10.8.0.0/16" />
                  </Form.Item>
                  <Form.Item
                    name="gateway_ip"
                    label="网关 IP"
                    rules={[{ required: !autoAllocate, message: '请输入网关 IP' }]}
                    extra="示例：10.8.0.1"
                  >
                    <Input placeholder="10.8.0.1" />
                  </Form.Item>
                </>
              )}
            </>
          )}
          <Form.Item name="description" label="描述">
            <Input.TextArea rows={3} placeholder="网络描述（可选）" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}
