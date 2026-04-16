import { Routes, Route, Navigate } from 'react-router-dom'
import { useAuthStore } from './store/auth'
import { Layout } from './components/Layout'
import { LoginPage } from './pages/Login'
import { DashboardPage } from './pages/Dashboard'
import { UsersPage } from './pages/Users'
import { UserDetailPage } from './pages/UserDetail'
import { NetworksPage } from './pages/Networks'
import { MoonsPage } from './pages/Moons'
import { MoonsDetailPage } from './pages/MoonsDetail'
import { DevicesPage } from './pages/Devices'
import { PoliciesPage } from './pages/Policies'
import { LogsPage } from './pages/Logs'
import { StatsPage } from './pages/Stats'
import { TrafficChartPage } from './pages/TrafficChart'
import { VersionsPage } from './pages/Versions'
import { DownloadsPage } from './pages/Downloads'
import { SettingsPage } from './pages/Settings'

function RequireAuth({ children }: { children: React.ReactNode }) {
  const { isAuthenticated } = useAuthStore()
  if (!isAuthenticated) {
    return <Navigate to="/login" replace />
  }
  return <>{children}</>
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        path="/"
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route index element={<DashboardPage />} />
        <Route path="users" element={<UsersPage />} />
        <Route path="users/:id" element={<UserDetailPage />} />
        <Route path="networks" element={<NetworksPage />} />
        <Route path="moons" element={<MoonsPage />} />
        <Route path="moons/:id" element={<MoonsDetailPage />} />
        <Route path="devices" element={<DevicesPage />} />
        <Route path="policies" element={<PoliciesPage />} />
        <Route path="logs" element={<LogsPage />} />
        <Route path="stats" element={<StatsPage />} />
        <Route path="stats/traffic" element={<TrafficChartPage />} />
        <Route path="versions" element={<VersionsPage />} />
        <Route path="downloads" element={<DownloadsPage />} />
        <Route path="settings" element={<SettingsPage />} />
      </Route>
    </Routes>
  )
}
