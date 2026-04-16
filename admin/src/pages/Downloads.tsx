import { useState, useEffect } from 'react'
import { Card, Row, Col, Tag, Space, Button, QRCode, Modal, message, Statistic } from 'antd'
import { DownloadOutlined, QrcodeOutlined, WindowsOutlined, AppleOutlined, LinuxOutlined, AndroidOutlined, MobileOutlined } from '@ant-design/icons'
import { get } from '../services/api'

interface ClientVersion {
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

const platformConfig: Record<string, { label: string; icon: React.ReactNode; color: string }> = {
  windows: { label: 'Windows', icon: <WindowsOutlined />, color: '#1890ff' },
  linux: { label: 'Linux', icon: <LinuxOutlined />, color: '#52c41a' },
  darwin: { label: 'macOS', icon: <AppleOutlined />, color: '#722ed1' },
  android: { label: 'Android', icon: <AndroidOutlined />, color: '#3f8600' },
  ios: { label: 'iOS', icon: <MobileOutlined />, color: '#faad14' },
}

const formatBytes = (bytes: number) => {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

export function DownloadsPage() {
  const [versions, setVersions] = useState<ClientVersion[]>([])
  const [, setLoading] = useState(false)
  const [qrVisible, setQrVisible] = useState(false)
  const [selectedVersion, setSelectedVersion] = useState<ClientVersion | null>(null)

  const fetchVersions = async () => {
    setLoading(true)
    try {
      const res = await get<ClientVersion[]>('/admin/versions/client')
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

  const handleDownload = (record: ClientVersion) => {
    window.open(record.download_url, '_blank')
  }

  const handleShowQR = (record: ClientVersion) => {
    setSelectedVersion(record)
    setQrVisible(true)
  }

  const getPlatformVersions = (platform: string) => {
    return versions.filter((v) => v.platform === platform && v.enabled)
  }

  const platforms = ['windows', 'darwin', 'linux', 'android', 'ios'] as const

  return (
    <div>
      <Row gutter={16} style={{ marginBottom: 24 }}>
        <Col span={4}>
          <Card><Statistic title="Windows 版本" value={getPlatformVersions('windows').length} /></Card>
        </Col>
        <Col span={4}>
          <Card><Statistic title="macOS 版本" value={getPlatformVersions('darwin').length} /></Card>
        </Col>
        <Col span={4}>
          <Card><Statistic title="Linux 版本" value={getPlatformVersions('linux').length} /></Card>
        </Col>
        <Col span={4}>
          <Card><Statistic title="Android 版本" value={getPlatformVersions('android').length} /></Card>
        </Col>
        <Col span={4}>
          <Card><Statistic title="iOS 版本" value={getPlatformVersions('ios').length} /></Card>
        </Col>
        <Col span={4}>
          <Card><Statistic title="总计版本" value={versions.length} /></Card>
        </Col>
      </Row>

      <Row gutter={16}>
        {platforms.map((platform) => {
          const config = platformConfig[platform]
          const platformVersions = getPlatformVersions(platform)
          return (
            <Col span={8} key={platform} style={{ marginBottom: 16 }}>
              <Card
                title={
                  <Space>
                    {config.icon}
                    {config.label}
                  </Space>
                }
                extra={
                  <Tag color={config.color}>
                    {platformVersions.length} 个版本
                  </Tag>
                }
              >
                {platformVersions.length === 0 ? (
                  <div style={{ textAlign: 'center', color: '#999' }}>暂无版本</div>
                ) : (
                  platformVersions.slice(0, 3).map((v) => (
                    <Card
                      key={v.id}
                      size="small"
                      style={{ marginBottom: 8 }}
                      bodyStyle={{ padding: 12 }}
                    >
                      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                        <Space direction="vertical" size="small">
                          <Space>
                            <Tag color="blue">{v.version}</Tag>
                            {v.force_update && <Tag color="red">强制更新</Tag>}
                          </Space>
                          <Space style={{ fontSize: 12, color: '#666' }}>
                            <span>{formatBytes(v.file_size)}</span>
                            <span>|</span>
                            <span>{v.arch}</span>
                            <span>|</span>
                            <span>{new Date(v.release_date).toLocaleDateString()}</span>
                          </Space>
                        </Space>
                        <Space>
                          <Button
                            type="text"
                            icon={<QrcodeOutlined />}
                            onClick={() => handleShowQR(v)}
                          />
                          <Button
                            type="primary"
                            size="small"
                            icon={<DownloadOutlined />}
                            onClick={() => handleDownload(v)}
                          >
                            下载
                          </Button>
                        </Space>
                      </div>
                    </Card>
                  ))
                )}
              </Card>
            </Col>
          )
        })}
      </Row>

      <Modal
        title="扫码下载"
        open={qrVisible}
        onCancel={() => setQrVisible(false)}
        footer={null}
        width={300}
      >
        {selectedVersion && (
          <div style={{ textAlign: 'center' }}>
            <QRCode
              value={selectedVersion.download_url}
              size={200}
              style={{ marginBottom: 16 }}
            />
            <div>
              <Tag color={platformConfig[selectedVersion.platform].color}>
                {platformConfig[selectedVersion.platform].icon} {platformConfig[selectedVersion.platform].label}
              </Tag>
            </div>
            <div style={{ marginTop: 8 }}>
              <strong>{selectedVersion.version}</strong>
            </div>
            <div style={{ fontSize: 12, color: '#666' }}>
              {formatBytes(selectedVersion.file_size)}
            </div>
          </div>
        )}
      </Modal>
    </div>
  )
}
