import { useState, useEffect } from 'react'
import { Table, Button, Tag, Space, Modal, Form, Input, Select, Upload, message } from 'antd'
import { DeleteOutlined, UploadOutlined } from '@ant-design/icons'
import { get, post, del } from '../services/api'
import type { PageParams } from '../types/api'

interface Version {
  id: string
  version: string
  min_supported: string
  platform: string
  force_update: boolean
  enabled: boolean
  file_size: number
  sha256: string
  download_url: string
  created_at: string
}

export function VersionsPage() {
  const [versions, setVersions] = useState<Version[]>([])
  const [loading, setLoading] = useState(false)
  const [modalVisible, setModalVisible] = useState(false)
  const [form] = Form.useForm()
  const [params] = useState<PageParams>({ page: 1, limit: 20 })
  const [uploading, setUploading] = useState(false)
  const [file, setFile] = useState<File | null>(null)

  const fetchVersions = async () => {
    setLoading(true)
    try {
      const res = await get<Version[]>('/admin/versions')
      setVersions(res.data || [])
    } catch (error) {
      message.error('获取版本列表失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchVersions()
  }, [])

  const handleDelete = async (id: string) => {
    try {
      await del(`/admin/versions/${id}`)
      message.success('删除成功')
      fetchVersions()
    } catch (error) {
      message.error('删除失败')
    }
  }

  const handleUpload = () => {
    setModalVisible(true)
  }

  const handleSubmit = async (values: Record<string, unknown>) => {
    if (!file) {
      message.error('请选择要上传的文件')
      return
    }
    setUploading(true)
    try {
      const formData = new FormData()
      formData.append('version', values.version as string)
      formData.append('min_supported', values.min_supported as string)
      formData.append('platform', values.platform as string)
      formData.append('force_update', String(values.force_update || false))
      formData.append('file', file)
      await post('/admin/versions', formData)
      message.success('上传成功')
      setModalVisible(false)
      setFile(null)
      form.resetFields()
      fetchVersions()
    } catch (error) {
      message.error('上传失败')
    } finally {
      setUploading(false)
    }
  }

  const platformMap: Record<string, { label: string; color: string }> = {
    linux: { label: 'Linux', color: 'blue' },
    windows: { label: 'Windows', color: 'green' },
    darwin: { label: 'macOS', color: 'purple' },
    android: { label: 'Android', color: 'orange' },
    ios: { label: 'iOS', color: 'red' },
  }

  const columns = [
    {
      title: '版本号',
      dataIndex: 'version',
      key: 'version',
      render: (v: string) => <Tag>{v}</Tag>,
    },
    {
      title: '最低支持版本',
      dataIndex: 'min_supported',
      key: 'min_supported',
    },
    {
      title: '平台',
      dataIndex: 'platform',
      key: 'platform',
      render: (p: string) => {
        const config = platformMap[p] || { label: p, color: 'default' }
        return <Tag color={config.color}>{config.label}</Tag>
      },
    },
    {
      title: '文件大小',
      dataIndex: 'file_size',
      key: 'file_size',
      render: (size: number) => `${(size / 1024 / 1024).toFixed(2)} MB`,
    },
    {
      title: '强制更新',
      dataIndex: 'force_update',
      key: 'force_update',
      render: (force: boolean) => (force ? <Tag color="red">是</Tag> : <Tag>否</Tag>),
    },
    {
      title: '状态',
      dataIndex: 'enabled',
      key: 'enabled',
      render: (enabled: boolean) => (enabled ? <Tag color="success">启用</Tag> : <Tag>禁用</Tag>),
    },
    {
      title: '上传时间',
      dataIndex: 'created_at',
      key: 'created_at',
      render: (t: string) => new Date(t).toLocaleString(),
    },
    {
      title: '操作',
      key: 'action',
      render: (_: unknown, record: Version) => (
        <Space>
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
        <Button type="primary" icon={<UploadOutlined />} onClick={handleUpload}>
          上传新版本
        </Button>
      </div>
      <Table
        columns={columns}
        dataSource={versions}
        rowKey="id"
        loading={loading}
        pagination={{
          current: params.page,
          pageSize: params.limit,
          total: 0,
        }}
      />
      <Modal
        title="上传新版本"
        open={modalVisible}
        onCancel={() => setModalVisible(false)}
        footer={null}
        width={500}
      >
        <Form form={form} layout="vertical" onFinish={handleSubmit}>
          <Form.Item
            name="version"
            label="版本号"
            rules={[{ required: true, message: '请输入版本号' }]}
            extra="示例：1.0.0"
          >
            <Input placeholder="1.0.0" />
          </Form.Item>
          <Form.Item
            name="min_supported"
            label="最低支持版本"
            rules={[{ required: true, message: '请输入最低支持版本' }]}
          >
            <Input placeholder="0.0.1" />
          </Form.Item>
          <Form.Item
            name="platform"
            label="目标平台"
            rules={[{ required: true, message: '请选择平台' }]}
          >
            <Select>
              <Select.Option value="linux">Linux</Select.Option>
              <Select.Option value="windows">Windows</Select.Option>
              <Select.Option value="darwin">macOS</Select.Option>
              <Select.Option value="android">Android</Select.Option>
              <Select.Option value="ios">iOS</Select.Option>
            </Select>
          </Form.Item>
          <Form.Item
            name="file"
            label="安装包文件"
            rules={[{ required: true, message: '请上传安装包' }]}
            extra="支持 .exe/.dmg/.deb/.apk 等格式，最大 500MB"
          >
            <Upload
              beforeUpload={(f) => {
                setFile(f)
                return false
              }}
              maxCount={1}
            >
              <Button icon={<UploadOutlined />}>选择文件</Button>
            </Upload>
          </Form.Item>
          <Form.Item name="force_update" label="强制更新">
            <Select>
              <Select.Option value={false}>否</Select.Option>
              <Select.Option value={true}>是</Select.Option>
            </Select>
          </Form.Item>
          <Form.Item>
            <Space>
              <Button type="primary" htmlType="submit" loading={uploading}>
                上传
              </Button>
              <Button onClick={() => setModalVisible(false)}>取消</Button>
            </Space>
          </Form.Item>
        </Form>
      </Modal>
    </div>
  )
}
