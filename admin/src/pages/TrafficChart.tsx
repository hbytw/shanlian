import { useEffect, useRef, useState } from 'react'
import { Card, Space, Segmented, Row, Col, Statistic } from 'antd'
import { ThunderboltOutlined, ArrowUpOutlined, ArrowDownOutlined } from '@ant-design/icons'
import { get } from '../services/api'
import * as echarts from 'echarts'

type TimeRange = '1h' | '24h' | '7d' | '30d'

interface TrafficData {
  timestamp: string
  traffic_in: number
  traffic_out: number
}

interface TrafficStats {
  total_in: number
  total_out: number
  peak_in: number
  peak_out: number
  avg_in: number
  avg_out: number
}

const formatBytes = (bytes: number) => {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

export function TrafficChartPage() {
  const chartRef = useRef<HTMLDivElement>(null)
  const chartInstanceRef = useRef<echarts.ECharts | null>(null)
  const [loading, setLoading] = useState(false)
  const [timeRange, setTimeRange] = useState<TimeRange>('24h')
  const [trafficData, setTrafficData] = useState<TrafficData[]>([])
  const [stats, setStats] = useState<TrafficStats | null>(null)

  useEffect(() => {
    fetchTrafficData()
  }, [timeRange])

  useEffect(() => {
    if (chartRef.current && trafficData.length > 0) {
      if (!chartInstanceRef.current) {
        chartInstanceRef.current = echarts.init(chartRef.current)
      }
      updateChart()
    }
    return () => {
      if (chartInstanceRef.current) {
        chartInstanceRef.current.dispose()
        chartInstanceRef.current = null
      }
    }
  }, [trafficData])

  const fetchTrafficData = async () => {
    setLoading(true)
    try {
      // 计算时间范围
      const now = new Date()
      let startTime: Date
      let groupBy = 'hour'
      switch (timeRange) {
        case '1h':
          startTime = new Date(now.getTime() - 60 * 60 * 1000)
          groupBy = 'hour'
          break
        case '24h':
          startTime = new Date(now.getTime() - 24 * 60 * 60 * 1000)
          groupBy = 'hour'
          break
        case '7d':
          startTime = new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000)
          groupBy = 'day'
          break
        case '30d':
          startTime = new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000)
          groupBy = 'day'
          break
      }
      const params = new URLSearchParams({
        start_time: startTime.toISOString(),
        end_time: now.toISOString(),
        group_by: groupBy,
      })
      const res = await get<TrafficData[]>(`/admin/stats/traffic?${params}`)
      const data = res.data || []
      setTrafficData(data)
      calculateStats(data)
    } catch (error) {
      console.error('获取流量数据失败')
    } finally {
      setLoading(false)
    }
  }

  const calculateStats = (data: TrafficData[]) => {
    if (data.length === 0) {
      setStats(null)
      return
    }
    const total_in = data.reduce((sum, d) => sum + d.traffic_in, 0)
    const total_out = data.reduce((sum, d) => sum + d.traffic_out, 0)
    const peak_in = Math.max(...data.map((d) => d.traffic_in))
    const peak_out = Math.max(...data.map((d) => d.traffic_out))
    setStats({
      total_in,
      total_out,
      peak_in,
      peak_out,
      avg_in: total_in / data.length,
      avg_out: total_out / data.length,
    })
  }

  const updateChart = () => {
    if (!chartInstanceRef.current || trafficData.length === 0) return

    const times = trafficData.map((d) => new Date(d.timestamp).toLocaleString())
    const inData = trafficData.map((d) => d.traffic_in / 1024 / 1024) // MB
    const outData = trafficData.map((d) => d.traffic_out / 1024 / 1024) // MB

    const option: echarts.EChartsOption = {
      tooltip: {
        trigger: 'axis',
        formatter: (params: unknown) => {
          const p = params as { name: string; seriesName: string; value: number }[]
          let result = `${p[0].name}<br/>`
          p.forEach((item) => {
            result += `${item.seriesName}: ${formatBytes(item.value * 1024 * 1024)}<br/>`
          })
          return result
        },
      },
      legend: {
        data: ['入站流量', '出站流量'],
      },
      grid: {
        left: '3%',
        right: '4%',
        bottom: '3%',
        containLabel: true,
      },
      xAxis: {
        type: 'category',
        boundaryGap: false,
        data: times,
        axisLabel: {
          rotate: timeRange === '1h' ? 0 : 45,
          fontSize: 10,
        },
      },
      yAxis: {
        type: 'value',
        name: '流量 (MB)',
        axisLabel: {
          formatter: (value: number) => formatBytes(value * 1024 * 1024),
        },
      },
      series: [
        {
          name: '入站流量',
          type: 'line',
          smooth: true,
          areaStyle: {
            color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
              { offset: 0, color: 'rgba(82, 196, 26, 0.5)' },
              { offset: 1, color: 'rgba(82, 196, 26, 0.1)' },
            ]),
          },
          itemStyle: { color: '#52c41a' },
          data: inData,
        },
        {
          name: '出站流量',
          type: 'line',
          smooth: true,
          areaStyle: {
            color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
              { offset: 0, color: 'rgba(255, 77, 79, 0.5)' },
              { offset: 1, color: 'rgba(255, 77, 79, 0.1)' },
            ]),
          },
          itemStyle: { color: '#ff4d4f' },
          data: outData,
        },
      ],
    }

    chartInstanceRef.current.setOption(option)
  }

  const timeRangeLabels: Record<TimeRange, string> = {
    '1h': '1 小时',
    '24h': '24 小时',
    '7d': '7 天',
    '30d': '30 天',
  }

  return (
    <div>
      <Space style={{ marginBottom: 16 }}>
        <Segmented
          value={timeRange}
          onChange={(value) => setTimeRange(value as TimeRange)}
          options={[
            { label: '1 小时', value: '1h' },
            { label: '24 小时', value: '24h' },
            { label: '7 天', value: '7d' },
            { label: '30 天', value: '30d' },
          ]}
        />
      </Space>

      <Row gutter={16} style={{ marginBottom: 16 }}>
        <Col span={8}>
          <Card loading={loading}>
            <Statistic
              title="累计入站"
              value={formatBytes(stats?.total_in || 0)}
              prefix={<ArrowUpOutlined style={{ color: '#52c41a' }} />}
            />
          </Card>
        </Col>
        <Col span={8}>
          <Card loading={loading}>
            <Statistic
              title="累计出站"
              value={formatBytes(stats?.total_out || 0)}
              prefix={<ArrowDownOutlined style={{ color: '#ff4d4f' }} />}
            />
          </Card>
        </Col>
        <Col span={8}>
          <Card loading={loading}>
            <Statistic
              title="峰值入站"
              value={formatBytes(stats?.peak_in || 0)}
              prefix={<ThunderboltOutlined style={{ color: '#722ed1' }} />}
            />
          </Card>
        </Col>
      </Row>

      <Row gutter={16} style={{ marginBottom: 16 }}>
        <Col span={12}>
          <Card loading={loading}>
            <Statistic title="平均入站" value={formatBytes(stats?.avg_in || 0)} />
          </Card>
        </Col>
        <Col span={12}>
          <Card loading={loading}>
            <Statistic title="平均出站" value={formatBytes(stats?.avg_out || 0)} />
          </Card>
        </Col>
      </Row>

      <Card title={`流量趋势（${timeRangeLabels[timeRange]}）`} loading={loading}>
        <div ref={chartRef} style={{ width: '100%', height: 400 }} />
      </Card>
    </div>
  )
}
