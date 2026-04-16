import { useState } from 'react'
import { Card, Tabs, Form, Input, Button, Space, message, Switch, Upload, Tag, Row, Col, InputNumber } from 'antd'
import { SaveOutlined, UploadOutlined, FileTextOutlined } from '@ant-design/icons'
import { post, put } from '../services/api'

interface PlatformConfig {
  platform_name: string
  platform_logo?: string
}

interface SmtpConfig {
  host: string
  port: number
  username: string
  password: string
  from_email: string
  use_tls: boolean
}

interface WebhookConfig {
  dingtalk?: string
  wecom?: string
  feishu?: string
}

interface CertConfig {
  ca_cert: string
  private_key: string
  certificate: string
}

export function SettingsPage() {
  const [platformForm] = Form.useForm<PlatformConfig>()
  const [smtpForm] = Form.useForm<SmtpConfig>()
  const [webhookForm] = Form.useForm<WebhookConfig>()
  const [certForm] = Form.useForm<CertConfig>()
  const [loading, setLoading] = useState(false)
  const [certUploaded, setCertUploaded] = useState(false)

  const handleSavePlatform = async (values: PlatformConfig) => {
    setLoading(true)
    try {
      await put('/admin/settings/platform', values)
      message.success('平台配置保存成功')
    } catch (error) {
      message.error('保存失败')
    } finally {
      setLoading(false)
    }
  }

  const handleSaveSmtp = async (values: SmtpConfig) => {
    setLoading(true)
    try {
      await put('/admin/settings/smtp', values)
      message.success('邮件配置保存成功')
    } catch (error) {
      message.error('保存失败')
    } finally {
      setLoading(false)
    }
  }

  const handleSaveWebhook = async (values: WebhookConfig) => {
    setLoading(true)
    try {
      await put('/admin/settings/webhook', values)
      message.success('Webhook 配置保存成功')
    } catch (error) {
      message.error('保存失败')
    } finally {
      setLoading(false)
    }
  }

  const handleSaveCert = async (values: CertConfig) => {
    setLoading(true)
    try {
      await put('/admin/settings/cert', values)
      message.success('证书配置保存成功')
    } catch (error) {
      message.error('保存失败')
    } finally {
      setLoading(false)
    }
  }

  const handleTestSmtp = async () => {
    try {
      await post('/admin/settings/smtp/test', smtpForm.getFieldsValue())
      message.success('测试邮件发送成功')
    } catch (error) {
      message.error('测试邮件发送失败')
    }
  }

  const handleTestWebhook = async (type: 'dingtalk' | 'wecom' | 'feishu') => {
    try {
      const values = webhookForm.getFieldsValue()
      await post(`/admin/settings/webhook/${type}/test`, { url: values[type] })
      message.success('测试消息发送成功')
    } catch (error) {
      message.error('测试消息发送失败')
    }
  }

  const tabItems = [
    {
      key: 'platform',
      label: '平台配置',
      children: (
        <Card>
          <Form form={platformForm} layout="vertical" onFinish={handleSavePlatform} initialValues={{
            platform_name: '闪连 VPN 管理平台',
          }}>
            <Form.Item name="platform_name" label="平台名称" rules={[{ required: true, message: '请输入平台名称' }]}>
              <Input placeholder="请输入平台名称" />
            </Form.Item>
            <Form.Item name="platform_logo" label="平台 Logo">
              <Upload showUploadList={false} beforeUpload={() => false}>
                <Button icon={<UploadOutlined />}>上传 Logo</Button>
              </Upload>
            </Form.Item>
            <Form.Item>
              <Space>
                <Button type="primary" icon={<SaveOutlined />} htmlType="submit" loading={loading}>
                  保存配置
                </Button>
              </Space>
            </Form.Item>
          </Form>
        </Card>
      ),
    },
    {
      key: 'smtp',
      label: '邮件配置',
      children: (
        <Card>
          <Form form={smtpForm} layout="vertical" onFinish={handleSaveSmtp} initialValues={{
            host: 'smtp.example.com',
            port: 587,
            use_tls: true,
          }}>
            <Row gutter={16}>
              <Col span={12}>
                <Form.Item name="host" label="SMTP 服务器" rules={[{ required: true, message: '请输入服务器地址' }]}>
                  <Input placeholder="smtp.example.com" />
                </Form.Item>
              </Col>
              <Col span={12}>
                <Form.Item name="port" label="端口" rules={[{ required: true, message: '请输入端口' }]}>
                  <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                </Form.Item>
              </Col>
            </Row>
            <Row gutter={16}>
              <Col span={12}>
                <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
                  <Input placeholder="noreply@example.com" />
                </Form.Item>
              </Col>
              <Col span={12}>
                <Form.Item name="password" label="密码">
                  <Input.Password placeholder="请输入密码" />
                </Form.Item>
              </Col>
            </Row>
            <Form.Item name="from_email" label="发件人邮箱" rules={[{ required: true, type: 'email', message: '请输入正确邮箱' }]}>
              <Input placeholder="noreply@example.com" />
            </Form.Item>
            <Form.Item name="use_tls" label="启用 TLS" valuePropName="checked">
              <Switch />
            </Form.Item>
            <Form.Item>
              <Space>
                <Button type="primary" icon={<SaveOutlined />} htmlType="submit" loading={loading}>
                  保存配置
                </Button>
                <Button onClick={handleTestSmtp}>发送测试邮件</Button>
              </Space>
            </Form.Item>
          </Form>
        </Card>
      ),
    },
    {
      key: 'webhook',
      label: 'Webhook 配置',
      children: (
        <Card>
          <Form form={webhookForm} layout="vertical" onFinish={handleSaveWebhook}>
            <Form.Item
              name="dingtalk"
              label={
                <Space>
                  钉钉 Webhook
                  <Tag color="blue">钉钉</Tag>
                </Space>
              }
              extra="在钉钉群中添加自定义机器人，获取 Webhook 地址"
            >
              <Input placeholder="https://oapi.dingtalk.com/robot/send?access_token=xxx" />
            </Form.Item>
            <Form.Item>
              <Button onClick={() => handleTestWebhook('dingtalk')} style={{ marginBottom: 24 }}>
                测试钉钉
              </Button>
            </Form.Item>

            <Form.Item
              name="wecom"
              label={
                <Space>
                  企业微信 Webhook
                  <Tag color="green">企微</Tag>
                </Space>
              }
              extra="在企业微信群中添加自定义机器人，获取 Webhook 地址"
            >
              <Input placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx" />
            </Form.Item>
            <Form.Item>
              <Button onClick={() => handleTestWebhook('wecom')} style={{ marginBottom: 24 }}>
                测试企微
              </Button>
            </Form.Item>

            <Form.Item
              name="feishu"
              label={
                <Space>
                  飞书 Webhook
                  <Tag color="cyan">飞书</Tag>
                </Space>
              }
              extra="在飞书群中添加自定义机器人，获取 Webhook 地址"
            >
              <Input placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/xxx" />
            </Form.Item>
            <Form.Item>
              <Space>
                <Button type="primary" icon={<SaveOutlined />} htmlType="submit" loading={loading}>
                  保存配置
                </Button>
                <Button onClick={() => handleTestWebhook('feishu')}>测试飞书</Button>
              </Space>
            </Form.Item>
          </Form>
        </Card>
      ),
    },
    {
      key: 'cert',
      label: 'CA 证书管理',
      children: (
        <Card>
          <Form form={certForm} layout="vertical" onFinish={handleSaveCert}>
            <Form.Item
              name="ca_cert"
              label="CA 证书"
              extra="上传 CA 证书文件（.crt 或 .pem 格式）"
            >
              <Upload
                showUploadList={{ showRemoveIcon: true }}
                beforeUpload={(file) => {
                  const reader = new FileReader()
                  reader.onload = (e) => {
                    certForm.setFieldValue('ca_cert', e.target?.result as string)
                    setCertUploaded(true)
                  }
                  reader.readAsText(file)
                  return false
                }}
              >
                <Button icon={<UploadOutlined />}>选择证书文件</Button>
              </Upload>
            </Form.Item>

            <Form.Item name="private_key" label="私钥" extra="上传私钥文件（.key 或 .pem 格式）">
              <Upload
                showUploadList={{ showRemoveIcon: true }}
                beforeUpload={(file) => {
                  const reader = new FileReader()
                  reader.onload = (e) => {
                    certForm.setFieldValue('private_key', e.target?.result as string)
                  }
                  reader.readAsText(file)
                  return false
                }}
              >
                <Button icon={<UploadOutlined />}>选择私钥文件</Button>
              </Upload>
            </Form.Item>

            <Form.Item name="certificate" label="服务器证书" extra="上传服务器证书文件（.crt 或 .pem 格式）">
              <Upload
                showUploadList={{ showRemoveIcon: true }}
                beforeUpload={(file) => {
                  const reader = new FileReader()
                  reader.onload = (e) => {
                    certForm.setFieldValue('certificate', e.target?.result as string)
                  }
                  reader.readAsText(file)
                  return false
                }}
              >
                <Button icon={<UploadOutlined />}>选择证书文件</Button>
              </Upload>
            </Form.Item>

            {certUploaded && (
              <Tag icon={<FileTextOutlined />} color="success" style={{ marginBottom: 16 }}>
                证书已加载
              </Tag>
            )}

            <Form.Item>
              <Space>
                <Button type="primary" icon={<SaveOutlined />} htmlType="submit" loading={loading}>
                  保存证书
                </Button>
                <Button onClick={() => post('/admin/settings/cert/verify')}>
                  验证证书
                </Button>
              </Space>
            </Form.Item>
          </Form>
        </Card>
      ),
    },
  ]

  return (
    <div>
      <Tabs items={tabItems} />
    </div>
  )
}
