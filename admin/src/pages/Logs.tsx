import { useState, useEffect, useCallback } from 'react'
import { Table, Tabs, DatePicker, Space, Button, Tag, Select, message, Modal } from 'antd'
import { DownloadOutlined, ReloadOutlined, ExclamationCircleOutlined } from '@ant-design/icons'
import { get, post } from '../services/api'
import type { PageParams } from '../types/api'

const { RangePicker } = DatePicker

type LogType = 'access' | 'traffic' | 'policy' | 'online' | 'admin'


export function LogsPage() {
  const [activeTab, setActiveTab] = useState<LogType>('access')
  const [loading, setLoading] = useState(false)
  const [exportLoading, setExportLoading] = useState(false)
  const [params, setParams] = useState<PageParams>({ page: 1, limit: 20 })
  const [dateRange, setDateRange] = useState<[string, string] | null>(null)
  const [dataSource, setDataSource] = useState<Record<string, unknown>[]>([])
  const [total, setTotal] = useState(0)

  const fetchLogs = useCallback(async () => {
    setLoading(true)
    try {
      const queryParams: Record<string, unknown> = { ...params }
      if (dateRange) {
        queryParams.start_time = dateRange[0]
        queryParams.end_time = dateRange[1]
      }
      const endpoint = `/admin/logs/${activeTab === 'admin' ? 'admin' : activeTab}`
      const res = await get<{ records?: Record<string, unknown>[] }>(endpoint, queryParams)
      const records = res.data?.records || []
      setDataSource(records)
      setTotal(res.meta?.total || records.length)
    } catch (error) {
      message.error('获取日志失败')
    } finally {
      setLoading(false)
    }
  }, [activeTab, params, dateRange])

  useEffect(() => {
    fetchLogs()
  }, [fetchLogs])

  const handleTabChange = (key: string) => {
    setActiveTab(key as LogType)
    setParams({ ...params, page: 1 })
    setDataSource([])
  }

  const handleExport = () => {
    Modal.confirm({
      title: '导出日志',
      icon: <ExclamationCircleOutlined />,
      content: (
        <Space direction="vertical">
          <span>选择导出格式：</span>
          <Select
            id="export-format"
            defaultValue="csv"
            style={{ width: 120 }}
            options={[
              { label: 'CSV', value: 'csv' },
              { label: 'JSON', value: 'json' },
              { label: 'Excel', value: 'xlsx' },
            ]}
          />
        </Space>
      ),
      onOk: async () => {
        const formatSelect = document.getElementById('export-format') as HTMLSelectElement
        const format = (formatSelect?.value || 'csv') as 'csv' | 'json' | 'xlsx'
        setExportLoading(true)
        try {
          const queryParams: Record<string, unknown> = {}
          if (dateRange) {
            queryParams.start_time = dateRange[0]
            queryParams.end_time = dateRange[1]
          }
          const res = await post<{ url?: string }>('/admin/logs/export', {
            log_type: activeTab,
            format,
            ...queryParams,
          })
          if (res.data?.url) {
            window.open(res.data.url, '_blank')
            message.success('导出开始，请等待下载')
          } else {
            message.info('导出功能开发中')
          }
        } catch (error) {
          message.error('导出失败')
        } finally {
          setExportLoading(false)
        }
      },
    })
  }

  const getColumns = () => {
    switch (activeTab) {
      case 'access':
        return [
          { title: '设备 ID', dataIndex: 'device_id', key: 'device_id', render: (id: string) => <Tag>{id?.slice(0, 8) || '-'}</Tag> },
          { title: 'URL', dataIndex: 'url', key: 'url', ellipsis: true },
          { title: '主机', dataIndex: 'host', key: 'host' },
          { title: '方法', dataIndex: 'method', key: 'method', render: (m: string) => <Tag>{m}</Tag> },
          { title: '状态码', dataIndex: 'status_code', key: 'status_code', render: (code: number) => {
            const color = code >= 400 ? 'red' : code >= 300 ? 'orange' : 'green'
            return <Tag color={color}>{code}</Tag>
          }},
          { title: '访问时间', dataIndex: 'accessed_at', key: 'accessed_at', render: (t: string) => new Date(t).toLocaleString() },
        ]
      case 'traffic':
        return [
          { title: '设备 ID', dataIndex: 'device_id', key: 'device_id', render: (id: string) => <Tag>{id?.slice(0, 8) || '-'}</Tag> },
          { title: '方向', dataIndex: 'direction', key: 'direction', render: (d: string) => d === 'in' ? <Tag color="success">入站</Tag> : <Tag color="error">出站</Tag> },
          { title: '流量(字节)', dataIndex: 'bytes', key: 'bytes', render: (b: number) => b?.toLocaleString() },
          { title: '包数', dataIndex: 'packets', key: 'packets', render: (p: number) => p?.toLocaleString() },
          { title: '协议', dataIndex: 'protocol', key: 'protocol' },
          { title: '时间', dataIndex: 'logged_at', key: 'logged_at', render: (t: string) => new Date(t).toLocaleString() },
        ]
      case 'policy':
        return [
          { title: '操作者', dataIndex: 'operator', key: 'operator' },
          { title: '策略名称', dataIndex: 'policy_name', key: 'policy_name' },
          { title: '操作', dataIndex: 'action', key: 'action', render: (a: string) => <Tag>{a}</Tag> },
          { title: '原因', dataIndex: 'reason', key: 'reason', ellipsis: true },
          { title: '时间', dataIndex: 'changed_at', key: 'changed_at', render: (t: string) => new Date(t).toLocaleString() },
        ]
      case 'online':
        return [
          { title: '设备 ID', dataIndex: 'device_id', key: 'device_id', render: (id: string) => <Tag>{id?.slice(0, 8) || '-'}</Tag> },
          { title: '操作', dataIndex: 'action', key: 'action', render: (a: string) => a === 'online' ? <Tag color="success">上线</Tag> : <Tag color="default">下线</Tag> },
          { title: '在线时长(秒)', dataIndex: 'duration_sec', key: 'duration_sec', render: (s: number) => s?.toLocaleString() },
          { title: '时间', dataIndex: 'logged_at', key: 'logged_at', render: (t: string) => new Date(t).toLocaleString() },
        ]
      case 'admin':
        return [
          { title: '操作者', dataIndex: 'operator_name', key: 'operator_name' },
          { title: '操作', dataIndex: 'action', key: 'action', render: (a: string) => <Tag>{a}</Tag> },
          { title: '资源类型', dataIndex: 'resource_type', key: 'resource_type' },
          { title: '资源名称', dataIndex: 'resource_name', key: 'resource_name' },
          { title: 'IP 地址', dataIndex: 'ip_address', key: 'ip_address' },
          { title: '时间', dataIndex: 'created_at', key: 'created_at', render: (t: string) => new Date(t).toLocaleString() },
        ]
      default:
        return []
    }
  }

  return (
    <div>
      <Tabs
        activeKey={activeTab}
        onChange={handleTabChange}
        items={[
          { key: 'access', label: '访问日志' },
          { key: 'traffic', label: '流量日志' },
          { key: 'policy', label: '策略变更' },
          { key: 'online', label: '上下线' },
          { key: 'admin', label: '管理员操作' },
        ]}
      />
      <div style={{ marginBottom: 16, display: 'flex', justifyContent: 'space-between' }}>
        <Space>
          <RangePicker
            showTime
            onChange={(dates) => {
              if (dates && dates[0] && dates[1]) {
                setDateRange([dates[0].toISOString(), dates[1].toISOString()])
              } else {
                setDateRange(null)
              }
            }}
          />
          <Button icon={<ReloadOutlined />} onClick={fetchLogs}>
            刷新
          </Button>
        </Space>
        <Space>
          <Select
            placeholder="每页条数"
            defaultValue={20}
            style={{ width: 120 }}
            onChange={(value) => setParams({ ...params, limit: value })}
            options={[
              { label: '20 条', value: 20 },
              { label: '50 条', value: 50 },
              { label: '100 条', value: 100 },
            ]}
          />
          <Button
            type="primary"
            icon={<DownloadOutlined />}
            onClick={handleExport}
            loading={exportLoading}
          >
            导出
          </Button>
        </Space>
      </div>
      <Table
        columns={getColumns()}
        dataSource={dataSource}
        rowKey="id"
        loading={loading}
        pagination={{
          current: params.page,
          pageSize: params.limit,
          total,
          onChange: (page, limit) => setParams({ ...params, page, limit }),
          showSizeChanger: false,
        }}
        scroll={{ x: 'max-content' }}
      />
    </div>
  )
}
