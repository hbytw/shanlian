// API 响应类型
export interface ApiResponse<T = unknown> {
  code?: number
  message: string
  data?: T
  meta?: {
    total: number
    page: number
    limit: number
  }
}

export interface ErrorResponse {
  error: {
    code: string
    message: string
    detail?: Record<string, unknown>
    request_id?: string
  }
}

// 用户类型
export interface User {
  id: string
  username: string
  email: string
  phone?: string
  role: 'super_admin' | 'network_admin' | 'user'
  enabled: boolean
  created_at: string
  updated_at: string
}

// 登录响应
export interface LoginResponse {
  access_token: string
  refresh_token: string
  expires_in: number
  token_type: string
  user?: User
}

// 网络类型
export interface Network {
  id: string
  name: string
  cidr: string
  gateway_ip: string
  description?: string
  enabled: boolean
  created_at: string
  updated_at: string
  created_by?: string
}

// 设备类型
export interface Device {
  id: string
  device_id: string
  user_id?: string
  hostname?: string
  public_ip?: string
  nat_type: string
  assigned_ip?: string
  online: boolean
  client_version?: string
  os_type?: string
  os_version?: string
  first_seen_at: string
  last_seen_at: string
  enabled: boolean
}

// Moon 节点类型
export interface MoonNode {
  id: string
  domain: string
  public_ip: string
  gateway_ip: string
  relay_port: number
  wss_port: number
  udp_port: number
  health_port: number
  weight: number
  online: boolean
  version?: string
  connections: number
  created_at: string
  updated_at: string
  last_heartbeat_at?: string
}

// 节点类型
export interface Peer {
  id: string
  device_id: string
  network_id: string
  public_ip?: string
  public_port?: number
  nat_type: string
  online: boolean
  assigned_ip: string
  hostname?: string
  tunnel_type: string
  created_at: string
  updated_at: string
  last_seen_at?: string
}

// 策略类型
export interface Policy {
  id: string
  network_id: string
  name: string
  policy_type: 'route' | 'dns' | 'url_filter' | 'traffic_limit' | 'time_range' | 'audit'
  config: Record<string, unknown>
  priority: number
  enabled: boolean
  created_at: string
  updated_at: string
  created_by?: string
}

// 分页参数
export interface PageParams {
  page?: number
  limit?: number
  search?: string
  sort?: string
  order?: 'asc' | 'desc'
  [key: string]: unknown
}

// 统计数据
export interface StatsRealtime {
  online_peers: number
  online_devices: number
  online_moons: number
  total_networks: number
  traffic_in: number
  traffic_out: number
}

// 用户统计
export interface UserStats {
  total_devices: number
  online_devices: number
  total_networks: number
  total_traffic_in: number
  total_traffic_out: number
  created_days: number
}

// 平台配置
export interface PlatformConfig {
  platform_name: string
  platform_url: string
  support_email: string
  max_devices_per_user: number
  session_timeout: number
}

// SMTP 配置
export interface SmtpConfig {
  host: string
  port: number
  username: string
  password: string
  from_email: string
  use_tls: boolean
}

// Webhook 配置
export interface WebhookConfig {
  type: 'dingtalk' | 'wecom' | 'feishu'
  enabled: boolean
  url: string
  secret?: string
}

// 证书
export interface Certificate {
  id: string
  domain: string
  cert_type: 'letsencrypt' | 'custom'
  expires_at: string
  auto_renew: boolean
  enabled: boolean
}

// 统计数据概览
export interface StatsOverview {
  total_users: number
  total_devices: number
  total_peers: number
  traffic_in_24h: number
  traffic_out_24h: number
  active_sessions: number
  online_peers_24h: number
}

// 客户端版本
export interface ClientVersion {
  id: string
  version: string
  platform: 'windows' | 'linux' | 'darwin' | 'android' | 'ios'
  arch: string
  file_size: number
  sha256: string
  download_url: string
  min_supported?: string
  release_date: string
  force_update: boolean
  enabled: boolean
  created_at: string
}

// Moon 统计数据
export interface MoonStats {
  connections: number
  traffic_in: number
  traffic_out: number
  cpu_usage: number
  memory_usage: number
}
