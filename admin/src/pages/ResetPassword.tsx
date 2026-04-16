import { useState } from 'react'
import { useNavigate, Link } from 'react-router-dom'
import { Form, Input, Button, Card, message, Typography } from 'antd'
import { MailOutlined, LockOutlined } from '@ant-design/icons'
import { post } from '../services/api'

const { Title } = Typography

export function ResetPasswordPage() {
  const [loading, setLoading] = useState(false)
  const [countdown, setCountdown] = useState(0)
  const [form] = Form.useForm()
  const navigate = useNavigate()

  const sendVerifyCode = async (email: string) => {
    setCountdown(60)
    const timer = setInterval(() => {
      setCountdown((c) => {
        if (c <= 1) {
          clearInterval(timer)
          return 0
        }
        return c - 1
      })
    }, 1000)
    try {
      await post('/auth/code/send', { email })
      message.success('验证码已发送')
    } catch (error) {
      message.error('发送验证码失败')
    }
  }

  const onReset = async (values: { email: string; code: string; password: string }) => {
    setLoading(true)
    try {
      await post('/auth/password/reset', values)
      message.success('密码重置成功，请使用新密码登录')
      navigate('/login')
    } catch (error: any) {
      message.error(error?.response?.data?.error?.message || '重置失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div style={{ height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#f0f2f5' }}>
      <Card style={{ width: 400 }}>
        <div style={{ textAlign: 'center', marginBottom: 24 }}>
          <Title level={2}>闪连 VPN</Title>
          <Title level={4} type="secondary">找回密码</Title>
        </div>
        <Form form={form} name="reset-password" onFinish={onReset} layout="vertical">
          <Form.Item
            name="email"
            rules={[
              { required: true, message: '请输入邮箱' },
              { type: 'email', message: '邮箱格式不正确' }
            ]}
          >
            <Input
              prefix={<MailOutlined />}
              placeholder="注册邮箱"
              size="large"
              suffix={
                <Button
                  type="link"
                  size="small"
                  disabled={countdown > 0}
                  onClick={() => {
                    const email = form.getFieldValue('email')
                    if (email) sendVerifyCode(email)
                  }}
                >
                  {countdown > 0 ? `${countdown}s` : '获取验证码'}
                </Button>
              }
            />
          </Form.Item>
          <Form.Item
            name="code"
            rules={[{ required: true, message: '请输入验证码' }]}
          >
            <Input prefix={<LockOutlined />} placeholder="验证码" size="large" />
          </Form.Item>
          <Form.Item
            name="password"
            rules={[
              { required: true, message: '请输入新密码' },
              { min: 8, message: '密码至少8位' }
            ]}
          >
            <Input.Password prefix={<LockOutlined />} placeholder="新密码" size="large" />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit" loading={loading} block size="large">
              重置密码
            </Button>
          </Form.Item>
        </Form>
        <div style={{ textAlign: 'center', marginTop: 16 }}>
          <Link to="/login">返回登录</Link>
        </div>
      </Card>
    </div>
  )
}
