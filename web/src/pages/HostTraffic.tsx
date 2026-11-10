import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { Alert, Button, Card, Col, Row, Segmented, Select, Skeleton, Space, Table, Tabs, Tag, Typography } from 'antd'
import { Bar, BarChart, CartesianGrid, Legend, ResponsiveContainer, Tooltip as ChartTooltip, XAxis, YAxis } from 'recharts'
import dayjs from 'dayjs'
import { api } from '../api'
import type { HostTrafficItem, HostTrafficPeer, HostTrafficRetention, HostTrafficSummary, Node, TrafficAuditStatus } from '../api'
import { useI18n } from '../i18n'
import { useToast } from '../toast'
import { EmptyState, FilterBar, Mono, PageHeader, StatTile } from '../ui'
import { COLOR_PRIMARY, SEMANTIC_HEX } from '../theme'
import { fmtBytes } from '../format'

const { Text } = Typography

// 24h/48h 按小时画；更长的区间按本地自然日聚合，避免上千根柱子。
const RANGES = [24, 48, 24 * 7, 24 * 30, 24 * 90]

interface Bucket {
  label: string
  rx: number
  tx: number
}

function bucketize(summary: HostTrafficSummary, hours: number): Bucket[] {
  const daily = hours > 48
  const map = new Map<string, Bucket>()
  for (const p of summary.series ?? []) {
    const t = dayjs.unix(p.hour)
    const label = daily ? t.format('MM-DD') : t.format('MM-DD HH:00')
    const b = map.get(label) ?? { label, rx: 0, tx: 0 }
    b.rx += p.nic_rx
    b.tx += p.nic_tx
    map.set(label, b)
  }
  return [...map.values()]
}

function byKind(totals: HostTrafficItem[] | null, kind: string) {
  return (totals ?? []).filter((x) => x.kind === kind)
}

export default function HostTraffic({ switcher }: { switcher: ReactNode }) {
  const { t } = useI18n()
  const toast = useToast()
  const [nodes, setNodes] = useState<Node[]>([])
  const [nodeId, setNodeId] = useState<number>()
  const [hours, setHours] = useState(24)
  const [summary, setSummary] = useState<HostTrafficSummary | null>(null)
  const [retention, setRetention] = useState<HostTrafficRetention | null>(null)
  const [audit, setAudit] = useState<TrafficAuditStatus | null>(null)
  const [loading, setLoading] = useState(false)
  const [installing, setInstalling] = useState(false)

  useEffect(() => {
    api
      .nodes()
      .then((list) => {
        const enabled = (list ?? []).filter((n) => n.enabled)
        setNodes(enabled)
        if (enabled.length) setNodeId((cur) => cur ?? enabled[0].id)
      })
      .catch((e: unknown) => toast.err(e))
  }, [toast])

  const load = useCallback(async () => {
    if (!nodeId) return
    setLoading(true)
    try {
      const res = await api.hostTraffic(nodeId, hours)
      setSummary(res.summary)
      setRetention(res.retention)
    } catch (e) {
      toast.err(e)
    } finally {
      setLoading(false)
    }
  }, [nodeId, hours, toast])

  useEffect(() => {
    load()
  }, [load])

  // 采集器状态要 SSH 上机读，单独加载，不阻塞图表。
  useEffect(() => {
    if (!nodeId) return
    let cancelled = false
    setAudit(null)
    api
      .trafficAuditStatus(nodeId)
      .then((s) => !cancelled && setAudit(s))
      .catch(() => !cancelled && setAudit(null))
    return () => {
      cancelled = true
    }
  }, [nodeId])

  const install = async () => {
    if (!nodeId) return
    setInstalling(true)
    try {
      await api.installTrafficAudit(nodeId)
      toast.ok(t('hostInstalled'))
      setAudit(await api.trafficAuditStatus(nodeId))
    } catch (e) {
      toast.err(e)
    } finally {
      setInstalling(false)
    }
  }

  const chart = useMemo(() => (summary ? bucketize(summary, hours) : []), [summary, hours])
  const nic = useMemo(() => {
    const item = (summary?.totals ?? []).find((x) => x.kind === 'interface')
    return { rx: item?.rx ?? 0, tx: item?.tx ?? 0 }
  }, [summary])

  const itemColumns = (nameTitle: string) => [
    { title: nameTitle, dataIndex: 'name', render: (v: string) => <Mono>{v}</Mono> },
    {
      title: t('hostRx'),
      align: 'right' as const,
      sorter: (a: HostTrafficItem, b: HostTrafficItem) => a.rx - b.rx,
      render: (_: unknown, r: HostTrafficItem) => <span style={{ color: SEMANTIC_HEX.down }}>{fmtBytes(r.rx)}</span>,
    },
    {
      title: t('hostTx'),
      align: 'right' as const,
      sorter: (a: HostTrafficItem, b: HostTrafficItem) => a.tx - b.tx,
      render: (_: unknown, r: HostTrafficItem) => <span style={{ color: SEMANTIC_HEX.up }}>{fmtBytes(r.tx)}</span>,
    },
    {
      title: t('hostTotal'),
      align: 'right' as const,
      defaultSortOrder: 'descend' as const,
      sorter: (a: HostTrafficItem, b: HostTrafficItem) => a.rx + a.tx - (b.rx + b.tx),
      render: (_: unknown, r: HostTrafficItem) => fmtBytes(r.rx + r.tx),
    },
    {
      title: '',
      width: 70,
      render: (_: unknown, r: HostTrafficItem) => (r.resets ? <Tag color="orange">reset {r.resets}</Tag> : null),
    },
  ]

  const peerTable = (rows: HostTrafficPeer[] | null, other?: { count: number; bytes: number }) => (
    <>
      <Table
        size="small"
        rowKey="address"
        pagination={{ pageSize: 20, hideOnSinglePage: true }}
        dataSource={rows ?? []}
        locale={{ emptyText: <EmptyState description={t('noData')} /> }}
        columns={[
          { title: t('hostAddress'), dataIndex: 'address', render: (v: string) => <Mono>{v}</Mono> },
          { title: t('hostTotal'), align: 'right', render: (_: unknown, r: HostTrafficPeer) => fmtBytes(r.bytes) },
          { title: t('hostPackets'), align: 'right', dataIndex: 'packets' },
        ]}
      />
      {other && other.count > 0 && (
        <Text type="secondary" style={{ fontSize: 12 }}>
          {t('hostOther', { count: other.count })}: {fmtBytes(other.bytes)}
        </Text>
      )}
    </>
  )

  const auditTag = () => {
    if (!audit) return null
    if (!audit.installed) return <Tag>{t('hostNotInstalled')}</Tag>
    const age = Math.round(audit.status?.last_sample_age ?? 0)
    return (
      <Space size={4} wrap>
        <Tag color={audit.healthy ? 'green' : 'red'}>
          {t('hostCollector')}: {audit.healthy ? t('hostHealthy') : `${t('hostUnhealthy')} · ${audit.reason ?? ''}`}
        </Tag>
        {audit.status?.last_sample ? <Text type="secondary">{t('hostLastSample', { age })}</Text> : null}
        {audit.up_to_date === false && <Tag color="orange">{t('hostOutdated')}</Tag>}
      </Space>
    )
  }

  const hasData = (summary?.hours ?? 0) > 0

  return (
    <>
      <PageHeader
        title={t('stats')}
        description={
          retention
            ? t('hostTrafficHint', {
                from: dayjs.unix(retention.hourly_from).format('YYYY-MM-DD'),
                days: retention.peers_days,
              })
            : undefined
        }
        extra={switcher}
      />

      <FilterBar>
        <Select
          style={{ minWidth: 160 }}
          value={nodeId}
          onChange={setNodeId}
          options={nodes.map((n) => ({ value: n.id, label: n.name }))}
        />
        <Segmented
          value={hours}
          onChange={(v) => setHours(Number(v))}
          options={RANGES.map((h) => ({ value: h, label: h <= 48 ? `${h}h` : `${h / 24}d` }))}
        />
        {auditTag()}
        {audit && (!audit.installed || audit.up_to_date === false || audit.healthy === false) && (
          <Button size="small" loading={installing} onClick={install}>
            {t('hostInstall')}
          </Button>
        )}
      </FilterBar>

      {loading && !summary ? (
        <Skeleton active />
      ) : !hasData ? (
        <Card>
          <EmptyState description={audit && !audit.installed ? t('hostNotInstalled') : t('noData')} />
        </Card>
      ) : (
        summary && (
          <>
            <Row gutter={[12, 12]} style={{ marginBottom: 12 }}>
              <Col xs={12} md={6}>
                <StatTile label={t('hostNicRx')} value={fmtBytes(nic.rx)} accent={SEMANTIC_HEX.down} />
              </Col>
              <Col xs={12} md={6}>
                <StatTile label={t('hostNicTx')} value={fmtBytes(nic.tx)} accent={SEMANTIC_HEX.up} />
              </Col>
              <Col xs={12} md={6}>
                <StatTile label={t('hostNicTotal')} value={fmtBytes(nic.rx + nic.tx)} accent={COLOR_PRIMARY} />
              </Col>
              <Col xs={12} md={6}>
                <StatTile
                  label={t('hostCoverage')}
                  value={`${summary.expected ? Math.round((summary.hours / summary.expected) * 100) : 0}%`}
                  foot={t('hostCoverageFoot', {
                    hours: summary.hours,
                    expected: summary.expected,
                    gap: Math.round(summary.max_gap),
                    resets: summary.resets,
                  })}
                  accent={summary.hours < summary.expected || summary.resets ? SEMANTIC_HEX.warn : SEMANTIC_HEX.down}
                />
              </Col>
            </Row>

            <Card size="small" style={{ marginBottom: 12 }}>
              <ResponsiveContainer width="100%" height={260}>
                <BarChart data={chart}>
                  <CartesianGrid strokeDasharray="3 3" vertical={false} />
                  <XAxis dataKey="label" tick={{ fontSize: 11 }} minTickGap={16} />
                  <YAxis tickFormatter={(v: number) => fmtBytes(v)} width={80} tick={{ fontSize: 11 }} />
                  <ChartTooltip formatter={(v) => fmtBytes(Number(v))} />
                  <Legend />
                  <Bar dataKey="rx" name={t('hostNicRx')} stackId="nic" fill={SEMANTIC_HEX.down} />
                  <Bar dataKey="tx" name={t('hostNicTx')} stackId="nic" fill={SEMANTIC_HEX.up} radius={[3, 3, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            </Card>

            <Tabs
              items={[
                {
                  key: 'service',
                  label: t('hostByService'),
                  children: (
                    <Table
                      size="small"
                      rowKey="name"
                      pagination={false}
                      dataSource={byKind(summary.totals, 'service')}
                      columns={itemColumns(t('hostService'))}
                    />
                  ),
                },
                {
                  key: 'port',
                  label: t('hostByPort'),
                  children: (
                    <Table
                      size="small"
                      rowKey="name"
                      pagination={false}
                      dataSource={byKind(summary.totals, 'port')}
                      columns={itemColumns(t('hostPort'))}
                    />
                  ),
                },
                {
                  key: 'peers',
                  label: t('hostPeers'),
                  children: (
                    <>
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        {t('hostPeersFrom', { from: dayjs.unix(summary.peers_from).format('YYYY-MM-DD HH:00') })}
                      </Text>
                      <Row gutter={12}>
                        <Col xs={24} lg={12}>
                          <Text strong>{t('hostRx')}</Text>
                          {peerTable(summary.top_peers.rx, summary.peer_other.rx)}
                        </Col>
                        <Col xs={24} lg={12}>
                          <Text strong>{t('hostTx')}</Text>
                          {peerTable(summary.top_peers.tx, summary.peer_other.tx)}
                        </Col>
                      </Row>
                    </>
                  ),
                },
                {
                  key: 'tcp',
                  label: t('hostTcp'),
                  children: (
                    <Table
                      size="small"
                      rowKey="name"
                      pagination={false}
                      dataSource={byKind(summary.totals, 'tcp')}
                      columns={[
                        { title: 'TCP', dataIndex: 'name', render: (v: string) => <Mono>{v}</Mono> },
                        {
                          title: t('hostPackets'),
                          align: 'right',
                          render: (_: unknown, r: HostTrafficItem) => (r.rx_packets + r.tx_packets).toLocaleString(),
                        },
                      ]}
                    />
                  ),
                },
              ]}
            />
            <Alert type="info" showIcon style={{ marginTop: 12 }} message={t('hostScopeNote')} />
          </>
        )
      )}
    </>
  )
}
