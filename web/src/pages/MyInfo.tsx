import { useEffect, useMemo, useState } from 'react'
import { Button, Card, Col, Descriptions, Input, List, Progress, Row, Skeleton, Space, Table, Typography } from 'antd'
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip as ChartTooltip, XAxis, YAxis } from 'recharts'
import { api } from '../api'
import type { MeInfo, Node, UsageRow } from '../api'
import { useI18n } from '../i18n'
import { useToast } from '../toast'
import { EmptyState, Mono, PageHeader } from '../ui'
import { SEMANTIC_HEX } from '../theme'
import { fmtBytes } from '../format'
import { daysBetween, shiftDay, upDownByDay } from '../usage'

const { Text } = Typography

export default function MyInfo() {
  const { t } = useI18n()
  const toast = useToast()
  const [me, setMe] = useState<MeInfo | null>(null)
  const [nodes, setNodes] = useState<Pick<Node, 'id' | 'name' | 'domain'>[]>([])
  const [usage, setUsage] = useState<UsageRow[]>([])
  const [range, setRange] = useState<[string, string] | null>(null)

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const [info, nodeList] = await Promise.all([api.me(), api.myNodes()])
        if (cancelled) return
        setMe(info)
        setNodes(nodeList ?? [])
        // 自助视图固定看近 30 天；越界的部分由后端按留存窗口裁剪
        const today = new Date().toISOString().slice(0, 10)
        const window: [string, string] = [shiftDay(today, -29), today]
        setRange(window)
        setUsage(await api.myUsage({ from: window[0], to: window[1], group: ['day', 'node'] }))
      } catch (error) {
        if (!cancelled) toast.err(error)
      }
    }
    load()
    return () => {
      cancelled = true
    }
  }, [toast])

  const chartData = useMemo(
    () => (range ? upDownByDay(usage, daysBetween(range[0], range[1])) : []),
    [usage, range],
  )

  if (!me) return <Skeleton active />

  const subUrl = `${location.origin}/sub/${me.sub_token}`
  const clashUrl = `${subUrl}?format=clash`
  const percent = me.traffic_limit_bytes
    ? Math.min(100, Math.round((me.traffic_used_bytes / me.traffic_limit_bytes) * 100))
    : 0

  const copy = async (value: string) => {
    await navigator.clipboard.writeText(value)
    toast.ok(t('copied'))
  }

  return (
    <>
      <PageHeader title={t('myInfo')} />
      <Row gutter={[14, 14]}>
        <Col xs={24} lg={12}>
          <Card size="small">
            <Descriptions column={1} size="small">
              <Descriptions.Item label={t('username')}>
                <Text strong>{me.name}</Text>
              </Descriptions.Item>
              <Descriptions.Item label={t('expires')}>
                <Mono>{me.expire_at ? me.expire_at.split(' ')[0] : t('never')}</Mono>
              </Descriptions.Item>
              <Descriptions.Item label={t('traffic')}>
                <Space direction="vertical" size={2} style={{ width: '100%' }}>
                  <Mono>
                    {fmtBytes(me.traffic_used_bytes)} /{' '}
                    {me.traffic_limit_bytes ? fmtBytes(me.traffic_limit_bytes) : t('unlimited')}
                  </Mono>
                  {me.traffic_limit_bytes > 0 && <Progress percent={percent} size="small" showInfo={false} />}
                </Space>
              </Descriptions.Item>
            </Descriptions>
          </Card>
        </Col>
        <Col xs={24} lg={12}>
          <Card size="small" title={t('subscription')}>
            <Space direction="vertical" size={8} style={{ width: '100%' }}>
              <Space.Compact style={{ width: '100%' }}>
                <Input readOnly value={subUrl} />
                <Button type="primary" onClick={() => copy(subUrl)}>
                  {t('copy')}
                </Button>
              </Space.Compact>
              <Space.Compact style={{ width: '100%' }}>
                <Input readOnly value={clashUrl} />
                <Button onClick={() => copy(clashUrl)}>Clash</Button>
              </Space.Compact>
            </Space>
          </Card>
        </Col>
        <Col xs={24} lg={12}>
          <Card size="small" title={t('myNodes')}>
            <List
              size="small"
              dataSource={nodes}
              locale={{ emptyText: <EmptyState description={t('noAccessibleNodes')} /> }}
              renderItem={(node) => (
                <List.Item>
                  <Text strong>{node.name}</Text>
                  <Mono>{node.domain}</Mono>
                </List.Item>
              )}
            />
          </Card>
        </Col>
        <Col xs={24} lg={12}>
          <Card size="small" title={t('myUsage')}>
            {usage.length === 0 ? (
              <EmptyState description={t('noData')} />
            ) : (
              <ResponsiveContainer width="100%" height={200}>
                <AreaChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 8 }}>
                  <CartesianGrid strokeDasharray="3 4" vertical={false} stroke="#eef0f3" />
                  <XAxis
                    dataKey="day"
                    tickLine={false}
                    axisLine={false}
                    fontSize={12}
                    tickFormatter={(day: string) => day.slice(5)}
                  />
                  <YAxis
                    tickLine={false}
                    axisLine={false}
                    fontSize={12}
                    width={92}
                    tickFormatter={(value: number) => fmtBytes(value)}
                  />
                  <ChartTooltip
                    formatter={(value, name) => [
                      fmtBytes(Number(value)),
                      String(name) === 'up' ? t('upload2') : t('download2'),
                    ]}
                  />
                  <Area type="monotone" dataKey="down" stackId="1" stroke={SEMANTIC_HEX.down} fill={SEMANTIC_HEX.down} fillOpacity={0.15} />
                  <Area type="monotone" dataKey="up" stackId="1" stroke={SEMANTIC_HEX.up} fill={SEMANTIC_HEX.up} fillOpacity={0.15} />
                </AreaChart>
              </ResponsiveContainer>
            )}
          </Card>
        </Col>
        <Col xs={24}>
          <Card size="small" title={t('nodesUsed')}>
            <Table
              size="small"
              rowKey={(row) => `${row.day}-${row.node_id}`}
              dataSource={[...usage].sort((a, b) => (b.day ?? '').localeCompare(a.day ?? ''))}
              pagination={{ pageSize: 15, size: 'small' }}
              locale={{ emptyText: <EmptyState description={t('noData')} /> }}
              columns={[
                { title: t('day'), dataIndex: 'day', width: 140 },
                { title: t('nodes'), dataIndex: 'node' },
                {
                  title: t('upload2'),
                  dataIndex: 'up',
                  width: 130,
                  align: 'right',
                  render: (value: number) => fmtBytes(value),
                },
                {
                  title: t('download2'),
                  dataIndex: 'down',
                  width: 130,
                  align: 'right',
                  render: (value: number) => fmtBytes(value),
                },
              ]}
            />
          </Card>
        </Col>
      </Row>
    </>
  )
}
