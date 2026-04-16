import { useState } from 'react'
import { Outlet, useNavigate, useLocation } from 'react-router-dom'
import { Layout as AntLayout, Menu, Avatar, Dropdown, Button, Typography } from 'antd'
import {
  DashboardOutlined,
  UserOutlined,
  GlobalOutlined,
  ClusterOutlined,
  MobileOutlined,
  SecurityScanOutlined,
  LogoutOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  DownloadOutlined,
  SettingOutlined,
  FileTextOutlined,
} from '@ant-design/icons'
import { useAuthStore } from '../store/auth'

const { Header, Sider, Content } = AntLayout
const { Title } = Typography

const menuItems = [
  { key: '/', icon: <DashboardOutlined />, label: '仪表盘' },
  { key: '/users', icon: <UserOutlined />, label: '用户管理' },
  { key: '/networks', icon: <GlobalOutlined />, label: '网络管理' },
  { key: '/moons', icon: <ClusterOutlined />, label: 'Moon 节点' },
  { key: '/devices', icon: <MobileOutlined />, label: '设备管理' },
  { key: '/policies', icon: <SecurityScanOutlined />, label: '策略管理' },
  { key: '/logs', icon: <FileTextOutlined />, label: '日志审计' },
  { key: '/stats', icon: <DashboardOutlined />, label: '监控统计' },
  { key: '/versions', icon: <DownloadOutlined />, label: '版本管理' },
  { key: '/downloads', icon: <DownloadOutlined />, label: '客户端下载' },
  { key: '/settings', icon: <SettingOutlined />, label: '系统设置' },
]

export function Layout() {
  const [collapsed, setCollapsed] = useState(false)
  const navigate = useNavigate()
  const location = useLocation()
  const { user, clearAuth } = useAuthStore()

  const handleLogout = () => {
    clearAuth()
    navigate('/login')
  }

  const userMenuItems = [
    {
      key: 'logout',
      icon: <LogoutOutlined />,
      label: '退出登录',
      danger: true,
      onClick: handleLogout,
    },
  ]

  return (
    <AntLayout style={{ minHeight: '100vh' }}>
      <Sider trigger={null} collapsible collapsed={collapsed}>
        <div style={{ height: 64, display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <Title level={4} style={{ color: '#fff', margin: 0 }}>
            {collapsed ? '闪连' : '闪连 VPN'}
          </Title>
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[location.pathname]}
          items={menuItems}
          onClick={({ key }) => navigate(key)}
        />
      </Sider>
      <AntLayout>
        <Header style={{ padding: '0 24px', background: '#fff', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <Button
            type="text"
            icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
            onClick={() => setCollapsed(!collapsed)}
          />
          <Dropdown menu={{ items: userMenuItems }} placement="bottomRight">
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
              <Avatar icon={<UserOutlined />} />
              <span>{user?.username || '管理员'}</span>
            </div>
          </Dropdown>
        </Header>
        <Content style={{ margin: 24, padding: 24, background: '#fff', borderRadius: 8 }}>
          <Outlet />
        </Content>
      </AntLayout>
    </AntLayout>
  )
}
