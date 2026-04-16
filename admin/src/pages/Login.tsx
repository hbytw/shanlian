import { useState } from 'react'
import { useNavigate, Link } from 'react-router-dom'
import { Form, Input, Button, Card, message, Typography, Tabs, Divider, Space } from 'antd'
import { UserOutlined, LockOutlined, MobileOutlined, MailOutlined } from '@ant-design/icons'
import { post } from '../services/api'
import { useAuthStore } from '../store/auth'
import type { LoginResponse } from '../types/api'

const { Title } = Typography

export function LoginPage() {
  const [loading, setLoading] = useState(false)
  const [sendingCode, setSendingCode] = useState(false)
  const [codeSent, setCodeSent] = useState(false)
  const [countdown, setCountdown] = useState(0)
  const navigate = useNavigate()
  const { setAuth } = useAuthStore()

  const onPasswordLogin = async (values: { username: string; password: string }) => {
    setLoading(true)
    try {
      const res = await post<LoginResponse>('/auth/login', values)
      if (res.data) {
        const { access_token, refresh_token, user } = res.data
        setAuth(access_token, refresh_token, user!)
        message.success('登录成功')
        navigate('/')
      }
    } catch (error) {
      message.error('用户名或密码错误')
    } finally {
      setLoading(false)
    }
  }

  const sendVerifyCode = async (target: string, type: 'phone' | 'email') => {
    setSendingCode(true)
    try {
      await post('/auth/code/send', type === 'phone' ? { phone: target } : { email: target })
      message.success('验证码已发送')
      setCodeSent(true)
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

  const onPhoneLogin = async (values: { phone: string; code: string }) => {
    setLoading(true)
    try {
      const res = await post<LoginResponse>('/auth/login/phone', values)
      if (res.data) {
        const { access_token, refresh_token, user } = res.data
        setAuth(access_token, refresh_token, user!)
        message.success('登录成功')
        navigate('/')
      }
    } catch (error) {
      message.error('验证码错误或已过期')
    } finally {
      setLoading(false)
    }
  }

  const onEmailLogin = async (values: { email: string; code: string }) => {
    setLoading(true)
    try {
      const res = await post<LoginResponse>('/auth/login/email', values)
      if (res.data) {
        const { access_token, refresh_token, user } = res.data
        setAuth(access_token, refresh_token, user!)
        message.success('登录成功')
        navigate('/')
      }
    } catch (error) {
      message.error('验证码错误或已过期')
    } finally {
      setLoading(false)
    }
  }

  const PasswordLoginForm = () => (
    <Form name="password-login" onFinish={onPasswordLogin} layout="vertical">
      <Form.Item
        name="username"
        rules={[{ required: true, message: '请输入用户名' }]}
      >
        <Input prefix={<UserOutlined />} placeholder="用户名" size="large" />
      </Form.Item>
      <Form.Item
        name="password"
        rules={[{ required: true, message: '请输入密码' }]}
      >
        <Input.Password prefix={<LockOutlined />} placeholder="密码" size="large" />
      </Form.Item>
      <Form.Item>
        <Button type="primary" htmlType="submit" loading={loading} block size="large">
          登录
        </Button>
      </Form.Item>
    </Form>
  )

  const PhoneLoginForm = () => {
    const [form] = Form.useForm()
    return (
      <Form form={form} name="phone-login" onFinish={onPhoneLogin} layout="vertical">
        <Form.Item
          name="phone"
          rules={[
            { required: true, message: '请输入手机号' },
            { pattern: /^1[3-9]\d{9}$/, message: '手机号格式不正确' }
          ]}
        >
          <Input prefix={<MobileOutlined />} placeholder="手机号" size="large" />
        </Form.Item>
        <Form.Item
          name="code"
          rules={[{ required: true, message: '请输入验证码' }]}
        >
          <Input
            prefix={<LockOutlined />}
            placeholder="验证码"
            size="large"
            suffix={
              <Button
                type="link"
                size="small"
                disabled={countdown > 0}
                onClick={() => {
                  const phone = form.getFieldValue('phone')
                  if (phone && /^1[3-9]\d{9}$/.test(phone)) {
                    sendVerifyCode(phone, 'phone')
                  } else {
                    message.error('请输入正确的手机号')
                  }
                }}
              >
                {countdown > 0 ? `${countdown}s` : '获取验证码'}
              </Button>
            }
          />
        </Form.Item>
        <Form.Item>
          <Button type="primary" htmlType="submit" loading={loading} block size="large">
            登录
          </Button>
        </Form.Item>
      </Form>
    )
  }

  const EmailLoginForm = () => {
    const [form] = Form.useForm()
    return (
      <Form form={form} name="email-login" onFinish={onEmailLogin} layout="vertical">
        <Form.Item
          name="email"
          rules={[
            { required: true, message: '请输入邮箱' },
            { type: 'email', message: '邮箱格式不正确' }
          ]}
        >
          <Input prefix={<MailOutlined />} placeholder="邮箱" size="large" />
        </Form.Item>
        <Form.Item
          name="code"
          rules={[{ required: true, message: '请输入验证码' }]}
        >
          <Input
            prefix={<LockOutlined />}
            placeholder="验证码"
            size="large"
            suffix={
              <Button
                type="link"
                size="small"
                disabled={countdown > 0}
                onClick={() => {
                  const email = form.getFieldValue('email')
                  if (email) {
                    sendVerifyCode(email, 'email')
                  } else {
                    message.error('请输入邮箱')
                  }
                }}
              >
                {countdown > 0 ? `${countdown}s` : '获取验证码'}
              </Button>
            }
          />
        </Form.Item>
        <Form.Item>
          <Button type="primary" htmlType="submit" loading={loading} block size="large">
            登录
          </Button>
        </Form.Item>
      </Form>
    )
  }

  return (
    <div style={{ height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#f0f2f5' }}>
      <Card style={{ width: 400 }}>
        <div style={{ textAlign: 'center', marginBottom: 24 }}>
          <Title level={2}>闪连 VPN</Title>
          <Title level={4} type="secondary">管理平台</Title>
        </div>
        <Tabs
          defaultActiveKey="password"
          items={[
            { key: 'password', label: '密码登录', children: <PasswordLoginForm /> },
            { key: 'phone', label: '手机登录', children: <PhoneLoginForm /> },
            { key: 'email', label: '邮箱登录', children: <EmailLoginForm /> },
          ]}
        />
        <Divider />
        <Space style={{ width: '100%', justifyContent: 'center' }}>
          <Link to="/register">注册账号</Link>
          <Link to="/reset-password">忘记密码</Link>
        </Space>
      </Card>
    </div>
  )
}
