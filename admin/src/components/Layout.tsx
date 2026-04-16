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

// 菜单配置
const menuItemsConfig = [
  { key: '/', icon: <DashboardOutlined />, label: '仪表盘', roles: ['super_admin', 'network_admin', 'user'] },
  { key: '/users', icon: <UserOutlined />, label: '用户管理', roles: ['super_admin'] },
  { key: '/networks', icon: <GlobalOutlined />, label: '网络管理', roles: ['super_admin', 'network_admin'] },
  { key: '/moons', icon: <ClusterOutlined />, label: 'Moon 节点', roles: ['super_admin', 'network_admin'] },
  { key: '/devices', icon: <MobileOutlined />, label: '设备管理', roles: ['super_admin', 'network_admin', 'user'] },
  { key: '/policies', icon: <SecurityScanOutlined />, label: '策略管理', roles: ['super_admin', 'network_admin'] },
  { key: '/logs', icon: <FileTextOutlined />, label: '日志审计', roles: ['super_admin', 'network_admin'] },
  { key: '/stats', icon: <DashboardOutlined />, label: '监控统计', roles: ['super_admin', 'network_admin', 'user'] },
  { key: '/versions', icon: <DownloadOutlined />, label: '版本管理', roles: ['super_admin'] },
  { key: '/downloads', icon: <DownloadOutlined />, label: '客户端下载', roles: ['super_admin', 'network_admin', 'user'] },
  { key: '/settings', icon: <SettingOutlined />, label: '系统设置', roles: ['super_admin'] },
]

export function Layout() {
  const [collapsed, setCollapsed] = useState(false)
  const navigate = useNavigate()
  const location = useLocation()
  const { user, clearAuth } = useAuthStore()
  const userRole = user?.role || 'user'

  // 根据角色过滤菜单
  const menuItems = menuItemsConfig
    .filter(item => item.roles.includes(userRole))
    .map(({ roles, ...rest }) => rest)

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
