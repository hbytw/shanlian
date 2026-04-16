import { useState } from 'react'
import { Form, Input, Select, Switch, Button, Space, Tag, message, Drawer, Descriptions, Divider } from 'antd'
import { SaveOutlined } from '@ant-design/icons'
import { post, put } from '../services/api'
import type { Policy } from '../types/api'

const policyTypeMap: Record<string, { label: string; color: string; configTemplate: Record<string, unknown> }> = {
  route: {
    label: '路由策略',
    color: 'blue',
    configTemplate: {
      action: 'allow',
      routes: [],
      mode: 'whitelist',
    },
  },
  dns: {
    label: 'DNS 策略',
    color: 'green',
    configTemplate: {
      action: 'allow',
      domains: [],
      fallback: 'system',
    },
  },
  url_filter: {
    label: 'URL 过滤',
    color: 'orange',
    configTemplate: {
      action: 'block',
      patterns: [],
      category: 'malware',
    },
  },
  traffic_limit: {
    label: '流量限制',
    color: 'red',
    configTemplate: {
      max_bytes: 0,
      period: 'daily',
      action: 'throttle',
    },
  },
  time_range: {
    label: '时间范围',
    color: 'purple',
    configTemplate: {
      allowed_hours: [],
      timezone: 'Asia/Shanghai',
    },
  },
  audit: {
    label: '审计策略',
    color: 'cyan',
    configTemplate: {
      log_level: 'info',
      events: [],
    },
  },
}

interface PolicyFormValues {
  name: string
  network_id: string
  policy_type: string
  priority: number
  enabled: boolean
  config: Record<string, unknown>
}

interface PolicyDetailDrawerProps {
  policyId: string | null
  mode: 'create' | 'edit' | 'view'
  visible: boolean
  onClose: () => void
  onSuccess: () => void
}

export function PolicyDetailDrawer({ policyId, mode, visible, onClose, onSuccess }: PolicyDetailDrawerProps) {
  const [form] = Form.useForm<PolicyFormValues>()
  const [loading, setLoading] = useState(false)
  const [policy] = useState<Policy | null>(null)
  const [configJson, setConfigJson] = useState('')

  const isView = mode === 'view'
  const isEdit = mode === 'edit'
  const isCreate = mode === 'create'

  const handleTypeChange = (type: string) => {
    const template = policyTypeMap[type]?.configTemplate || {}
    form.setFieldValue('config', template)
    setConfigJson(JSON.stringify(template, null, 2))
  }

  const handleConfigJsonChange = (value: string) => {
    setConfigJson(value)
    try {
      const parsed = JSON.parse(value)
      form.setFieldValue('config', parsed)
    } catch {
      // ignore
    }
  }

  const handleSubmit = async () => {
    try {
      const values = await form.validateFields()
      setLoading(true)
      if (isCreate) {
        await post('/admin/policies', values)
        message.success('策略创建成功')
      } else if (isEdit && policyId) {
        await put(`/admin/policies/${policyId}`, values)
        message.success('策略更新成功')
      }
      onSuccess()
      onClose()
    } catch (error) {
      message.error(isCreate ? '创建失败' : '更新失败')
    } finally {
      setLoading(false)
    }
  }

  const renderConfigEditor = () => {
    const policyType = form.getFieldValue('policy_type')
    if (!policyType) return null

    const config = policyTypeMap[policyType]
    if (!config) return null

    switch (policyType) {
      case 'route':
        return (
          <>
            <Form.Item name={['config', 'action']} label="动作">
              <Select>
                <Select.Option value="allow">允许</Select.Option>
                <Select.Option value="deny">拒绝</Select.Option>
              </Select>
            </Form.Item>
            <Form.Item name={['config', 'mode']} label="模式">
              <Select>
                <Select.Option value="whitelist">白名单</Select.Option>
                <Select.Option value="blacklist">黑名单</Select.Option>
              </Select>
            </Form.Item>
            <Form.Item name={['config', 'routes']} label="路由列表">
              <Input.TextArea rows={4} placeholder="每行一条路由，如：10.0.0.0/8" />
            </Form.Item>
          </>
        )

      case 'dns':
        return (
          <>
            <Form.Item name={['config', 'action']} label="动作">
              <Select>
                <Select.Option value="allow">允许</Select.Option>
                <Select.Option value="block">屏蔽</Select.Option>
                <Select.Option value="redirect">重定向</Select.Option>
              </Select>
            </Form.Item>
            <Form.Item name={['config', 'fallback']} label="回退 DNS">
              <Select>
                <Select.Option value="system">系统默认</Select.Option>
                <Select.Option value="google">Google DNS</Select.Option>
                <Select.Option value="cloudflare">Cloudflare DNS</Select.Option>
              </Select>
            </Form.Item>
            <Form.Item name={['config', 'domains']} label="域名列表">
              <Input.TextArea rows={4} placeholder="每行一个域名，如：*.example.com" />
            </Form.Item>
          </>
        )

      case 'url_filter':
        return (
          <>
            <Form.Item name={['config', 'action']} label="动作">
              <Select>
                <Select.Option value="block">屏蔽</Select.Option>
                <Select.Option value="allow">允许</Select.Option>
                <Select.Option value="warn">警告</Select.Option>
              </Select>
            </Form.Item>
            <Form.Item name={['config', 'category']} label="类别">
              <Select>
                <Select.Option value="malware">恶意软件</Select.Option>
                <Select.Option value="phishing">钓鱼网站</Select.Option>
                <Select.Option value="gambling">赌博</Select.Option>
                <Select.Option value="adult">成人内容</Select.Option>
                <Select.Option value="custom">自定义</Select.Option>
              </Select>
            </Form.Item>
            <Form.Item name={['config', 'patterns']} label="URL 匹配规则">
              <Input.TextArea rows={4} placeholder="每行一条规则，支持通配符 *" />
            </Form.Item>
          </>
        )

      case 'traffic_limit':
        return (
          <>
            <Form.Item name={['config', 'max_bytes']} label="最大流量（字节）">
              <Input type="number" placeholder="1073741824" />
            </Form.Item>
            <Form.Item name={['config', 'period']} label="周期">
              <Select>
                <Select.Option value="daily">每天</Select.Option>
                <Select.Option value="weekly">每周</Select.Option>
                <Select.Option value="monthly">每月</Select.Option>
              </Select>
            </Form.Item>
            <Form.Item name={['config', 'action']} label="超出后动作">
              <Select>
                <Select.Option value="throttle">限速</Select.Option>
                <Select.Option value="block">阻断</Select.Option>
              </Select>
            </Form.Item>
          </>
        )

      case 'time_range':
        return (
          <>
            <Form.Item name={['config', 'timezone']} label="时区">
              <Select>
                <Select.Option value="Asia/Shanghai">北京时间</Select.Option>
                <Select.Option value="UTC">UTC</Select.Option>
                <Select.Option value="America/New_York">美东时间</Select.Option>
              </Select>
            </Form.Item>
            <Form.Item name={['config', 'allowed_hours']} label="允许时段">
              <Input.TextArea rows={4} placeholder='格式：{"start": "09:00", "end": "18:00"}，每行一个时段' />
            </Form.Item>
          </>
        )

      case 'audit':
        return (
          <>
            <Form.Item name={['config', 'log_level']} label="日志级别">
              <Select>
                <Select.Option value="debug">调试</Select.Option>
                <Select.Option value="info">信息</Select.Option>
                <Select.Option value="warn">警告</Select.Option>
                <Select.Option value="error">错误</Select.Option>
              </Select>
            </Form.Item>
            <Form.Item name={['config', 'events']} label="审计事件">
              <Select mode="multiple">
                <Select.Option value="login">登录</Select.Option>
                <Select.Option value="logout">登出</Select.Option>
                <Select.Option value="config_change">配置变更</Select.Option>
                <Select.Option value="network_access">网络访问</Select.Option>
                <Select.Option value="device_connect">设备连接</Select.Option>
              </Select>
            </Form.Item>
          </>
        )

      default:
        return (
          <Form.Item name="config" label="配置（JSON）">
            <Input.TextArea
              rows={8}
              value={configJson}
              onChange={(e) => handleConfigJsonChange(e.target.value)}
            />
          </Form.Item>
        )
    }
  }

  return (
    <Drawer
      title={
        <Space>
          {isCreate && '创建策略'}
          {isEdit && '编辑策略'}
          {isView && '策略详情'}
        </Space>
      }
      placement="right"
      width={600}
      open={visible}
      onClose={onClose}
      extra={
        !isView && (
          <Button type="primary" icon={<SaveOutlined />} onClick={handleSubmit} loading={loading}>
            保存
          </Button>
        )
      }
    >
      {isView && policy ? (
        <>
          <Descriptions column={2} bordered size="small">
            <Descriptions.Item label="策略名称">{policy.name}</Descriptions.Item>
            <Descriptions.Item label="策略类型">
              <Tag color={policyTypeMap[policy.policy_type]?.color}>
                {policyTypeMap[policy.policy_type]?.label || policy.policy_type}
              </Tag>
            </Descriptions.Item>
            <Descriptions.Item label="优先级">{policy.priority}</Descriptions.Item>
            <Descriptions.Item label="状态">
              <Tag color={policy.enabled ? 'success' : 'default'}>
                {policy.enabled ? '启用' : '禁用'}
              </Tag>
            </Descriptions.Item>
            <Descriptions.Item label="网络 ID">{policy.network_id}</Descriptions.Item>
            <Descriptions.Item label="创建时间">
              {new Date(policy.created_at).toLocaleString()}
            </Descriptions.Item>
          </Descriptions>
          <Divider>策略配置</Divider>
          <pre style={{ background: '#f5f5f5', padding: 16, borderRadius: 4 }}>
            {JSON.stringify(policy.config, null, 2)}
          </pre>
        </>
      ) : (
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="策略名称" rules={[{ required: true, message: '请输入策略名称' }]}>
            <Input placeholder="例如：屏蔽恶意网站" disabled={isView} />
          </Form.Item>

          <Form.Item name="policy_type" label="策略类型" rules={[{ required: true, message: '请选择策略类型' }]}>
            <Select onChange={handleTypeChange} disabled={isView || isEdit}>
              {Object.entries(policyTypeMap).map(([key, { label, color }]) => (
                <Select.Option key={key} value={key}>
                  <Tag color={color}>{label}</Tag>
                </Select.Option>
              ))}
            </Select>
          </Form.Item>

          <Form.Item name="network_id" label="关联网络" rules={[{ required: true, message: '请选择网络' }]}>
            <Input placeholder="网络 ID" disabled={isView} />
          </Form.Item>

          <Form.Item name="priority" label="优先级" initialValue={0}>
            <Input type="number" disabled={isView} />
          </Form.Item>

          <Form.Item name="enabled" label="启用状态" valuePropName="checked" initialValue={true}>
            <Switch disabled={isView} />
          </Form.Item>

          <Divider>策略配置</Divider>

          {renderConfigEditor()}
        </Form>
      )}
    </Drawer>
  )
}
