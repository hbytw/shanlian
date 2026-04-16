import { useEffect, useState } from 'react'
import { Table, Button, Space, Tag, Modal, Form, InputNumber, message, Input } from 'antd'
import { PlusOutlined, EditOutlined, DeleteOutlined, ClusterOutlined, InfoCircleOutlined } from '@ant-design/icons'
import { get, post, put, del } from '../services/api'
import { MoonDetailDrawer } from './MoonsDetail'
import type { MoonNode, PageParams } from '../types/api'

export function MoonsPage() {
  const [moons, setMoons] = useState<MoonNode[]>([])
  const [loading, setLoading] = useState(false)
  const [modalVisible, setModalVisible] = useState(false)
  const [editingMoon, setEditingMoon] = useState<MoonNode | null>(null)
  const [detailVisible, setDetailVisible] = useState(false)
  const [selectedMoonId, setSelectedMoonId] = useState<string | null>(null)
  const [form] = Form.useForm()
  const [params, setParams] = useState<PageParams>({ page: 1, limit: 20 })

  const fetchMoons = async () => {
    setLoading(true)
    try {
      const res = await get<MoonNode[]>('/admin/moons', params)
      setMoons(res.data || [])
    } catch (error) {
      message.error('获取 Moon 节点列表失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchMoons()
  }, [params])

  const handleAdd = () => {
    setEditingMoon(null)
    form.resetFields()
    setModalVisible(true)
  }

  const handleEdit = (moon: MoonNode) => {
    setEditingMoon(moon)
    form.setFieldsValue(moon)
    setModalVisible(true)
  }

  const handleDetail = (moon: MoonNode) => {
    setSelectedMoonId(moon.id)
    setDetailVisible(true)
  }

  const handleDelete = async (id: string) => {
    try {
      await del(`/admin/moons/${id}`)
      message.success('删除成功')
      fetchMoons()
    } catch (error) {
      message.error('删除失败')
    }
  }

  const handleSubmit = async (values: Record<string, unknown>) => {
    try {
      if (editingMoon) {
        await put(`/admin/moons/${editingMoon.id}`, values)
        message.success('更新成功')
      } else {
        await post('/admin/moons', values)
        message.success('创建成功')
      }
      setModalVisible(false)
      fetchMoons()
    } catch (error) {
      message.error(editingMoon ? '更新失败' : '创建失败')
    }
  }

  const columns = [
    { title: '域名', dataIndex: 'domain', key: 'domain', render: (domain: string) => <Tag icon={<ClusterOutlined />}>{domain}</Tag> },
    { title: '公网 IP', dataIndex: 'public_ip', key: 'public_ip' },
    { title: '网关 IP', dataIndex: 'gateway_ip', key: 'gateway_ip' },
    {
      title: '在线状态',
      dataIndex: 'online',
      key: 'online',
      render: (online: boolean) => (online ? <Tag color="success">在线</Tag> : <Tag color="default">离线</Tag>),
    },
    { title: '权重', dataIndex: 'weight', key: 'weight' },
    { title: '连接数', dataIndex: 'connections', key: 'connections' },
    { title: '版本', dataIndex: 'version', key: 'version' },
    {
      title: '端口',
      key: 'ports',
      render: (_: unknown, record: MoonNode) => (
        <Space direction="vertical" size="small">
          <Tag>中继: {record.relay_port}</Tag>
          <Tag>WSS: {record.wss_port}</Tag>
          <Tag>UDP: {record.udp_port}</Tag>
          <Tag>健康检查: {record.health_port}</Tag>
        </Space>
      ),
    },
    {
      title: '操作',
      key: 'action',
      render: (_: unknown, record: MoonNode) => (
        <Space>
          <Button type="link" icon={<InfoCircleOutlined />} onClick={() => handleDetail(record)}>
            详情
          </Button>
          <Button type="link" icon={<EditOutlined />} onClick={() => handleEdit(record)}>
            编辑
          </Button>
          <Button type="link" danger icon={<DeleteOutlined />} onClick={() => handleDelete(record.id)}>
            删除
          </Button>
        </Space>
      ),
    },
  ]

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 16 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={handleAdd}>
          添加 Moon 节点
        </Button>
      </div>
      <Table columns={columns} dataSource={moons} rowKey="id" loading={loading} pagination={{
        current: params.page,
        pageSize: params.limit,
        total: 0,
        onChange: (page, limit) => setParams({ ...params, page, limit }),
      }} />
      <Modal
        title={editingMoon ? '编辑 Moon 节点' : '添加 Moon 节点'}
        open={modalVisible}
        onCancel={() => setModalVisible(false)}
        onOk={() => form.submit()}
        width={600}
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit}>
          <Form.Item
            name="domain"
            label="域名"
            rules={[{ required: true, message: '请输入域名' }]}
            extra="示例：moon1.ytw.fr"
          >
            <Input placeholder="moon1.ytw.fr" />
          </Form.Item>
          <Form.Item name="public_ip" label="公网 IP" rules={[{ required: true, message: '请输入公网 IP' }]}>
            <Input placeholder="203.0.113.1" />
          </Form.Item>
          <Form.Item name="gateway_ip" label="网关 IP" rules={[{ required: true, message: '请输入网关 IP' }]}>
            <Input placeholder="10.8.0.1" />
          </Form.Item>
          <Space style={{ width: '100%' }}>
            <Form.Item name="relay_port" label="中继端口" initialValue={11002}>
              <InputNumber min={1} max={65535} />
            </Form.Item>
            <Form.Item name="wss_port" label="WSS 端口" initialValue={11443}>
              <InputNumber min={1} max={65535} />
            </Form.Item>
            <Form.Item name="udp_port" label="UDP 端口" initialValue={11001}>
              <InputNumber min={1} max={65535} />
            </Form.Item>
            <Form.Item name="health_port" label="健康检查端口" initialValue={11003}>
              <InputNumber min={1} max={65535} />
            </Form.Item>
          </Space>
          <Form.Item name="weight" label="权重" initialValue={100}>
            <InputNumber min={1} max={1000} />
          </Form.Item>
        </Form>
      </Modal>
      <MoonDetailDrawer
        moonId={selectedMoonId}
        visible={detailVisible}
        onClose={() => setDetailVisible(false)}
      />
    </div>
  )
}
