import { useState } from 'react'
import { useNavigate, Link } from 'react-router-dom'
import { Form, Input, Button, Card, message, Typography } from 'antd'
import { UserOutlined, LockOutlined, MailOutlined, MobileOutlined } from '@ant-design/icons'
import { post } from '../services/api'

const { Title } = Typography

export function RegisterPage() {
  const [loading, setLoading] = useState(false)
  const [sendingCode, setSendingCode] = useState(false)
  const [countdown, setCountdown] = useState(0)
  const [form] = Form.useForm()
  const navigate = useNavigate()

  const sendVerifyCode = async (target: string, type: 'email' | 'phone') => {
    setSendingCode(true)
    try {
      await post('/auth/code/send', type === 'email' ? { email: target } : { phone: target })
      message.success('验证码已发送')
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
    } catch (error) {
      message.error('发送验证码失败')
    } finally {
      setSendingCode(false)
    }
  }

  const onRegister = async (values: {
    username: string
    email: string
    phone?: string
    password: string
    code: string
  }) => {
    setLoading(true)
    try {
      await post('/auth/register', values)
      message.success('注册成功，请登录')
      navigate('/login')
    } catch (error: any) {
      message.error(error?.response?.data?.error?.message || '注册失败')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div style={{ height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#f0f2f5' }}>
      <Card style={{ width: 450 }}>
        <div style={{ textAlign: 'center', marginBottom: 24 }}>
          <Title level={2}>闪连 VPN</Title>
          <Title level={4} type="secondary">注册账号</Title>
        </div>
        <Form name="register" onFinish={onRegister} layout="vertical">
          <Form.Item
            name="username"
            rules={[
              { required: true, message: '请输入用户名' },
              { min: 3, max: 64, message: '用户名长度3-64字符' }
            ]}
          >
            <Input prefix={<UserOutlined />} placeholder="用户名" size="large" />
          </Form.Item>
          <Form.Item
            name="email"
            rules={[
              { required: true, message: '请输入邮箱' },
              { type: 'email', message: '邮箱格式不正确' }
            ]}
          >
            <Input
              prefix={<MailOutlined />}
              placeholder="邮箱"
              size="large"
              suffix={
                <Button
                  type="link"
                  size="small"
                  disabled={countdown > 0 || sendingCode}
                  onClick={() => {
                    const email = form.getFieldValue('email')
                    if (email) sendVerifyCode(email, 'email')
                  }}
                >
                  {countdown > 0 ? `${countdown}s` : '获取验证码'}
                </Button>
              }
            />
          </Form.Item>
          <Form.Item name="phone">
            <Input
              prefix={<MobileOutlined />}
              placeholder="手机号（可选）"
              size="large"
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
              { required: true, message: '请输入密码' },
              { min: 8, message: '密码至少8位' }
            ]}
          >
            <Input.Password prefix={<LockOutlined />} placeholder="密码" size="large" />
          </Form.Item>
          <Form.Item
            name="confirmPassword"
            dependencies={['password']}
            rules={[
              { required: true, message: '请确认密码' },
              ({ getFieldValue }) => ({
                validator(_, value) {
                  if (!value || getFieldValue('password') === value) {
                    return Promise.resolve()
                  }
                  return Promise.reject(new Error('两次密码不一致'))
                },
              }),
            ]}
          >
            <Input.Password prefix={<LockOutlined />} placeholder="确认密码" size="large" />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit" loading={loading} block size="large">
              注册
            </Button>
          </Form.Item>
        </Form>
        <div style={{ textAlign: 'center', marginTop: 16 }}>
          已有账号？<Link to="/login">立即登录</Link>
        </div>
      </Card>
    </div>
  )
}
