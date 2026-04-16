import { useEffect, useState } from 'react'
import { Table, Tag, Space, Button, Input, Modal, Form, Select, message, Popconfirm, Switch } from 'antd'
import { PlusOutlined, EditOutlined, DeleteOutlined, InfoCircleOutlined } from '@ant-design/icons'
import { get, post, put, patch, del } from '../services/api'
import { PolicyDetailDrawer } from './PoliciesDetail'
import type { Policy, PageParams } from '../types/api'

const policyTypeMap: Record<string, { label: string; color: string }> = {
  route: { label: '路由策略', color: 'blue' },
  dns: { label: 'DNS 策略', color: 'green' },
  url_filter: { label: 'URL 过滤', color: 'orange' },
  traffic_limit: { label: '流量限制', color: 'red' },
  time_range: { label: '时间范围', color: 'purple' },
  audit: { label: '审计策略', color: 'cyan' },
}

export function PoliciesPage() {
  const [policies, setPolicies] = useState<Policy[]>([])
  const [loading, setLoading] = useState(false)
  const [modalVisible, setModalVisible] = useState(false)
  const [detailVisible, setDetailVisible] = useState(false)
  const [editingPolicy, setEditingPolicy] = useState<Policy | null>(null)
  const [viewPolicy, setViewPolicy] = useState<Policy | null>(null)
  const [detailMode, setDetailMode] = useState<'create' | 'edit' | 'view'>('create')
  const [form] = Form.useForm()
  const [params, setParams] = useState<PageParams>({ page: 1, limit: 20 })

  const fetchPolicies = async () => {
    setLoading(true)
    try {
      const res = await get<Policy[]>('/admin/policies', params)
      setPolicies(res.data || [])
    } catch (error) {
      message.error('获取策略列表失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchPolicies()
  }, [params])

  const handleAdd = () => {
    setEditingPolicy(null)
    setDetailMode('create')
    form.resetFields()
    setModalVisible(true)
  }

  const handleEdit = (policy: Policy) => {
    setEditingPolicy(policy)
    setDetailMode('edit')
    form.setFieldsValue(policy)
    setModalVisible(true)
  }

  const handleView = (policy: Policy) => {
    setViewPolicy(policy)
    setDetailMode('view')
    setDetailVisible(true)
  }

  const handleDelete = async (id: string) => {
    try {
      await del(`/admin/policies/${id}`)
      message.success('删除成功')
      fetchPolicies()
    } catch (error) {
      message.error('删除失败')
    }
  }

  const handleToggle = async (id: string, enabled: boolean) => {
    try {
      await patch(`/admin/policies/${id}/status`, { enabled })
      message.success(enabled ? '启用成功' : '禁用成功')
      fetchPolicies()
    } catch (error) {
      message.error('操作失败')
    }
  }

  const handleSubmit = async (values: Record<string, unknown>) => {
    try {
      if (editingPolicy) {
        await put(`/admin/policies/${editingPolicy.id}`, values)
        message.success('更新成功')
      } else {
        await post('/admin/policies', values)
        message.success('创建成功')
      }
      setModalVisible(false)
      fetchPolicies()
    } catch (error) {
      message.error(editingPolicy ? '更新失败' : '创建失败')
    }
  }

  const columns = [
    { title: '策略名称', dataIndex: 'name', key: 'name' },
    {
      title: '策略类型',
      dataIndex: 'policy_type',
      key: 'policy_type',
      render: (type: string) => {
        const config = policyTypeMap[type] || { label: type, color: 'default' }
        return <Tag color={config.color}>{config.label}</Tag>
      },
    },
    { title: '优先级', dataIndex: 'priority', key: 'priority' },
    {
      title: '状态',
      dataIndex: 'enabled',
      key: 'enabled',
      render: (enabled: boolean, record: Policy) => (
        <Switch checked={enabled} onChange={(checked) => handleToggle(record.id, checked)} />
      ),
    },
    { title: '创建时间', dataIndex: 'created_at', key: 'created_at', render: (t: string) => new Date(t).toLocaleString() },
    {
      title: '操作',
      key: 'action',
      render: (_: unknown, record: Policy) => (
        <Space>
          <Button type="link" icon={<InfoCircleOutlined />} onClick={() => handleView(record)}>
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
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 16 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={handleAdd}>
          创建策略
        </Button>
      </div>
      <Table columns={columns} dataSource={policies} rowKey="id" loading={loading} pagination={{
        current: params.page,
        pageSize: params.limit,
        total: 0,
        onChange: (page, limit) => setParams({ ...params, page, limit }),
      }} />
      <Modal
        title={editingPolicy ? '编辑策略' : '创建策略'}
        open={modalVisible}
        onCancel={() => setModalVisible(false)}
        onOk={() => form.submit()}
        width={600}
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit}>
          <Form.Item name="name" label="策略名称" rules={[{ required: true, message: '请输入策略名称' }]}>
            <Input placeholder="例如：屏蔽恶意网站" />
          </Form.Item>
          <Form.Item name="policy_type" label="策略类型" rules={[{ required: true, message: '请选择策略类型' }]}>
            <Select>
              {Object.entries(policyTypeMap).map(([key, { label }]) => (
                <Select.Option key={key} value={key}>{label}</Select.Option>
              ))}
            </Select>
          </Form.Item>
          <Form.Item name="priority" label="优先级" initialValue={0}>
            <Input type="number" placeholder="数值越大优先级越高" />
          </Form.Item>
          <Form.Item name="config" label="策略配置" rules={[{ required: true, message: '请输入策略配置' }]}>
            <Input.TextArea rows={4} placeholder='{"action": "block", "urls": ["*.malware.com"]}' />
          </Form.Item>
        </Form>
      </Modal>
      <PolicyDetailDrawer
        policyId={viewPolicy?.id || editingPolicy?.id || null}
        mode={detailMode}
        visible={detailVisible}
        onClose={() => setDetailVisible(false)}
        onSuccess={fetchPolicies}
      />
    </div>
  )
}
