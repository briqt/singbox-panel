import { useCallback, useEffect, useMemo, useState } from 'react'
import { Card, Col, DatePicker, Row, Segmented, Select, Skeleton, Space, Table, Tabs, Tag, Button, Typography } from 'antd'
import { DownloadOutlined } from '@ant-design/icons'
import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  ResponsiveContainer,
  Tooltip as ChartTooltip,
  XAxis,
  YAxis,
} from 'recharts'
import dayjs from 'dayjs'
import type { Dayjs } from 'dayjs'
import { api } from '../api'
import type { Node, StatsMeta, UsageRow, User } from '../api'
import { useI18n } from '../i18n'
import type { Translate } from '../i18n'
import { useToast } from '../toast'
import type { ColumnType } from 'antd/es/table'
import { EmptyState, FilterBar, PageHeader, StatTile } from '../ui'
import { COLOR_PRIMARY, SEMANTIC_HEX, seriesColor } from '../theme'
import { downloadCsv, fmtBytes } from '../format'
import { daysBetween, firstOfMonth, laterDay, shiftDay, stackByDay, sumBytes, upDownByDay } from '../usage'

const { Text } = Typography

type StackDim = 'none' | 'user' | 'node'

// 汇总行：某用户（或节点）在区间内的合计，partnerList 是对手维度的分布
interface Grouped {
  id: number
  name: string
  up: number
  down: number
  total: number
  partnerList: [string, number][]
}

// 图例/提示里的序列名：上下行两条固定序列走词表，其余是用户名或节点名
const seriesLabel = (key: string, t: Translate) =>
  key === 'up' ? t('upload2') : key === 'down' ? t('download2') : key

// 字节列（单一写法）：右对齐、可排序、按取值函数读数，避免各表各写一遍
function bytesColumn<T>(title: string, get: (row: T) => number, color?: string): ColumnType<T> {
  return {
    title,
    width: 130,
    align: 'right',
    sorter: (a: T, b: T) => get(a) - get(b),
    render: (_: unknown, row: T) => <span style={{ color }}>{fmtBytes(get(row))}</span>,
  }
}

// 明细行（日 × 用户 × 节点）是本页唯一数据源，卡片 / 图表 / 汇总表都是它的求和视图，
// 因此各处口径不可能互相矛盾。
export default function Stats() {
  const { t } = useI18n()
  const toast = useToast()
  const [meta, setMeta] = useState<StatsMeta | null>(null)
  const [users, setUsers] = useState<User[]>([])
  const [nodes, setNodes] = useState<Node[]>([])
  const [range, setRange] = useState<[string, string] | null>(null)
  const [userId, setUserId] = useState<number>()
  const [nodeId, setNodeId] = useState<number>()
  const [stack, setStack] = useState<StackDim>('none')
  const [rows, setRows] = useState<UsageRow[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const [statsMeta, userList, nodeList] = await Promise.all([
          api.statsMeta(),
          api.users(),
          api.nodes(),
        ])
        if (cancelled) return
        setMeta(statsMeta)
        setUsers(userList ?? [])
        setNodes(nodeList ?? [])
        setRange([
          laterDay(shiftDay(statsMeta.today, -6), statsMeta.retention_from),
          statsMeta.today,
        ])
      } catch (error) {
        if (!cancelled) toast.err(error)
      }
    }
    load()
    return () => {
      cancelled = true
    }
  }, [toast])

  useEffect(() => {
    if (!range) return
    let cancelled = false
    setLoading(true)
    api
      .usage({ from: range[0], to: range[1], group: ['day', 'user', 'node'], user_id: userId, node_id: nodeId })
      .then((data) => {
        if (!cancelled) setRows(data)
      })
      .catch((error) => {
        if (!cancelled) toast.err(error)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [range, userId, nodeId, toast])

  const days = useMemo(() => (range ? daysBetween(range[0], range[1]) : []), [range])

  const totals = useMemo(() => {
    const up = rows.reduce((sum, row) => sum + row.up, 0)
    const down = rows.reduce((sum, row) => sum + row.down, 0)
    return {
      up,
      down,
      activeUsers: new Set(rows.map((row) => row.user_id)).size,
      activeNodes: new Set(rows.map((row) => row.node_id)).size,
    }
  }, [rows])

  const chart = useMemo(() => {
    if (stack === 'none') {
      return { data: upDownByDay(rows, days), keys: ['up', 'down'] }
    }
    return stackByDay(rows, days, (row) => (stack === 'user' ? (row.user ?? '') : (row.node ?? '')))
  }, [rows, days, stack])

  // 一个用户 / 节点在区间内的汇总，附带对手维度的明细（谁用了哪些节点）
  const groupBy = useCallback(
    (dimension: 'user' | 'node') => {
      const keyOf = (row: UsageRow) => (dimension === 'user' ? row.user_id : row.node_id) ?? 0
      const nameOf = (row: UsageRow) => (dimension === 'user' ? row.user : row.node) ?? ''
      const otherName = (row: UsageRow) => (dimension === 'user' ? row.node : row.user) ?? ''
      const buckets = new Map<
        number,
        { id: number; name: string; up: number; down: number; partners: Map<string, number> }
      >()
      for (const row of rows) {
        const id = keyOf(row)
        const bucket = buckets.get(id) ?? { id, name: nameOf(row), up: 0, down: 0, partners: new Map() }
        bucket.up += row.up
        bucket.down += row.down
        bucket.partners.set(otherName(row), (bucket.partners.get(otherName(row)) ?? 0) + row.up + row.down)
        buckets.set(id, bucket)
      }
      return [...buckets.values()]
        .map((bucket) => ({
          ...bucket,
          total: bucket.up + bucket.down,
          partnerList: [...bucket.partners.entries()].sort((a, b) => b[1] - a[1]),
        }))
        .sort((a, b) => b.total - a.total)
    },
    [rows],
  )

  const byUser = useMemo(() => groupBy('user'), [groupBy])
  const byNode = useMemo(() => groupBy('node'), [groupBy])
  const grandTotal = totals.up + totals.down

  const exportCsv = () => {
    const header = [t('day'), t('user'), t('nodes'), t('upload2'), t('download2')]
    const body = rows.map((row) => [row.day ?? '', row.user ?? '', row.node ?? '', row.up, row.down])
    downloadCsv(`usage-${range?.[0]}_${range?.[1]}.csv`, [header, ...body])
  }

  if (!meta || !range) return <Skeleton active />

  return (
    <>
      <PageHeader
        title={t('stats')}
        description={t('retentionHint', {
          months: meta.retention_months,
          from: meta.retention_from,
          tz: meta.timezone,
        })}
        extra={
          <Button icon={<DownloadOutlined />} onClick={exportCsv} disabled={!rows.length}>
            {t('export')}
          </Button>
        }
      />

      <FilterBar>
        <DatePicker.RangePicker
          allowClear={false}
          value={[dayjs(range[0]), dayjs(range[1])]}
          disabledDate={(current: Dayjs) =>
            current.format('YYYY-MM-DD') < meta.retention_from || current.format('YYYY-MM-DD') > meta.today
          }
          presets={[
            { label: t('last7'), value: [dayjs(shiftDay(meta.today, -6)), dayjs(meta.today)] },
            {
              label: t('last30'),
              value: [dayjs(laterDay(shiftDay(meta.today, -29), meta.retention_from)), dayjs(meta.today)],
            },
            { label: t('thisMonth'), value: [dayjs(firstOfMonth(meta.today)), dayjs(meta.today)] },
          ]}
          onChange={(value) => {
            if (value?.[0] && value?.[1]) {
              setRange([value[0].format('YYYY-MM-DD'), value[1].format('YYYY-MM-DD')])
            }
          }}
        />
        <Select
          allowClear
          style={{ minWidth: 160 }}
          placeholder={t('filterUser')}
          value={userId}
          onChange={setUserId}
          options={users.map((user) => ({ value: user.id, label: user.name }))}
        />
        <Select
          allowClear
          style={{ minWidth: 160 }}
          placeholder={t('filterNode')}
          value={nodeId}
          onChange={setNodeId}
          options={nodes.map((node) => ({ value: node.id, label: node.name }))}
        />
      </FilterBar>

      <Row gutter={[14, 14]} style={{ marginBottom: 16 }}>
        <Col xs={24} sm={12} lg={6}>
          <StatTile
            label={t('rangeTotal')}
            value={fmtBytes(grandTotal)}
            foot={`${t('dailyAvg')} ${fmtBytes(days.length ? Math.round(grandTotal / days.length) : 0)}`}
            accent={COLOR_PRIMARY}
          />
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <StatTile label={t('rangeUp')} value={fmtBytes(totals.up)} accent={SEMANTIC_HEX.up} />
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <StatTile label={t('rangeDown')} value={fmtBytes(totals.down)} accent={SEMANTIC_HEX.down} />
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <StatTile
            label={t('activeUsers')}
            value={totals.activeUsers}
            foot={`${t('activeNodes')} ${totals.activeNodes} / ${nodes.length}`}
            accent={SEMANTIC_HEX.warn}
          />
        </Col>
      </Row>

      <Card
        size="small"
        title={t('dailyTrend')}
        style={{ marginBottom: 16 }}
        extra={
          <Segmented
            size="small"
            value={stack}
            onChange={(value) => setStack(value as StackDim)}
            options={[
              { value: 'none', label: t('stackNone') },
              { value: 'user', label: t('stackUser') },
              { value: 'node', label: t('stackNode') },
            ]}
          />
        }
      >
        {loading ? (
          <Skeleton active />
        ) : rows.length === 0 ? (
          <EmptyState description={t('noData')} />
        ) : (
          <ResponsiveContainer width="100%" height={260}>
            <BarChart data={chart.data} margin={{ top: 8, right: 8, bottom: 0, left: 8 }}>
              <CartesianGrid strokeDasharray="3 4" vertical={false} stroke="#eef0f3" />
              <XAxis dataKey="day" tickLine={false} axisLine={false} fontSize={12} tickFormatter={(d: string) => d.slice(5)} />
              <YAxis tickLine={false} axisLine={false} fontSize={12} tickFormatter={(v: number) => fmtBytes(v)} width={80} />
              <ChartTooltip
                formatter={(value, name) => [fmtBytes(Number(value)), seriesLabel(String(name), t)]}
              />
              <Legend formatter={(value: string) => seriesLabel(value, t)} />
              {chart.keys.map((key, index) => (
                <Bar
                  key={key}
                  dataKey={key}
                  stackId="usage"
                  fill={
                    stack === 'none'
                      ? key === 'up'
                        ? SEMANTIC_HEX.up
                        : SEMANTIC_HEX.down
                      : seriesColor(index)
                  }
                  radius={index === chart.keys.length - 1 ? [3, 3, 0, 0] : undefined}
                />
              ))}
            </BarChart>
          </ResponsiveContainer>
        )}
      </Card>

      <Tabs
        items={[
          {
            key: 'user',
            label: t('perUser'),
            children: (
              <Table
                size="small"
                rowKey="id"
                loading={loading}
                dataSource={byUser}
                pagination={false}
                locale={{ emptyText: <EmptyState description={t('noData')} /> }}
                expandable={{
                  expandedRowRender: (record) => (
                    <DailyBreakdown rows={rows.filter((row) => row.user_id === record.id)} dimension="node" />
                  ),
                }}
                columns={[
                  { title: t('user'), dataIndex: 'name', render: (name: string) => <Text strong>{name}</Text> },
                  bytesColumn<Grouped>(t('upload2'), (row) => row.up, SEMANTIC_HEX.up),
                  bytesColumn<Grouped>(t('download2'), (row) => row.down, SEMANTIC_HEX.down),
                  bytesColumn<Grouped>(t('rangeTotal'), (row) => row.total),
                  {
                    title: t('usage'),
                    width: 110,
                    render: (_, record) =>
                      `${grandTotal ? Math.round((record.total / grandTotal) * 100) : 0}%`,
                  },
                  {
                    title: t('nodesUsed'),
                    dataIndex: 'partnerList',
                    render: (partners: [string, number][]) => (
                      <Space size={[4, 4]} wrap>
                        {partners.map(([name, bytes]) => (
                          <Tag key={name} bordered={false}>
                            {name} · {fmtBytes(bytes)}
                          </Tag>
                        ))}
                      </Space>
                    ),
                  },
                ]}
              />
            ),
          },
          {
            key: 'node',
            label: t('perNode'),
            children: (
              <Table
                size="small"
                rowKey="id"
                loading={loading}
                dataSource={byNode}
                pagination={false}
                locale={{ emptyText: <EmptyState description={t('noData')} /> }}
                expandable={{
                  expandedRowRender: (record) => (
                    <DailyBreakdown rows={rows.filter((row) => row.node_id === record.id)} dimension="user" />
                  ),
                }}
                columns={[
                  { title: t('nodes'), dataIndex: 'name', render: (name: string) => <Text strong>{name}</Text> },
                  bytesColumn<Grouped>(t('upload2'), (row) => row.up, SEMANTIC_HEX.up),
                  bytesColumn<Grouped>(t('download2'), (row) => row.down, SEMANTIC_HEX.down),
                  bytesColumn<Grouped>(t('rangeTotal'), (row) => row.total),
                  {
                    title: t('usersOnNode'),
                    dataIndex: 'partnerList',
                    render: (partners: [string, number][]) => (
                      <Space size={[4, 4]} wrap>
                        {partners.map(([name, bytes]) => (
                          <Tag key={name} bordered={false}>
                            {name} · {fmtBytes(bytes)}
                          </Tag>
                        ))}
                      </Space>
                    ),
                  },
                ]}
              />
            ),
          },
          {
            key: 'detail',
            label: t('perUserDay'),
            children: (
              <Table
                size="small"
                rowKey={(row) => `${row.day}-${row.user_id}-${row.node_id}`}
                loading={loading}
                dataSource={rows}
                pagination={{ pageSize: 50, showSizeChanger: true, size: 'small' }}
                locale={{ emptyText: <EmptyState description={t('noData')} /> }}
                columns={[
                  {
                    title: t('day'),
                    dataIndex: 'day',
                    width: 130,
                    sorter: (a: UsageRow, b: UsageRow) => (a.day ?? '').localeCompare(b.day ?? ''),
                  },
                  { title: t('user'), dataIndex: 'user', width: 160 },
                  { title: t('nodes'), dataIndex: 'node', width: 160 },
                  bytesColumn<UsageRow>(t('upload2'), (row) => row.up, SEMANTIC_HEX.up),
                  bytesColumn<UsageRow>(t('download2'), (row) => row.down, SEMANTIC_HEX.down),
                  bytesColumn<UsageRow>(t('rangeTotal'), (row) => row.up + row.down),
                ]}
              />
            ),
          },
        ]}
      />
    </>
  )
}

// 展开行：某个用户（或节点）在区间内逐日的对手维度明细
function DailyBreakdown({ rows, dimension }: { rows: UsageRow[]; dimension: 'user' | 'node' }) {
  const { t } = useI18n()
  const sorted = [...rows].sort((a, b) => (b.day ?? '').localeCompare(a.day ?? ''))
  return (
    <Table
      size="small"
      rowKey={(row) => `${row.day}-${row.user_id}-${row.node_id}`}
      dataSource={sorted}
      pagination={sorted.length > 10 ? { pageSize: 10, size: 'small' } : false}
      locale={{ emptyText: <EmptyState description={t('noData')} /> }}
      columns={[
        { title: t('day'), dataIndex: 'day', width: 130 },
        { title: dimension === 'node' ? t('nodes') : t('user'), dataIndex: dimension, width: 160 },
        {
          title: t('upload2'),
          dataIndex: 'up',
          width: 130,
          align: 'right',
          render: (value: number) => <span style={{ color: SEMANTIC_HEX.up }}>{fmtBytes(value)}</span>,
        },
        {
          title: t('download2'),
          dataIndex: 'down',
          width: 130,
          align: 'right',
          render: (value: number) => <span style={{ color: SEMANTIC_HEX.down }}>{fmtBytes(value)}</span>,
        },
        {
          title: t('rangeTotal'),
          width: 130,
          align: 'right',
          render: (_, row) => fmtBytes(row.up + row.down),
        },
      ]}
      summary={() => (
        <Table.Summary.Row>
          <Table.Summary.Cell index={0} colSpan={4}>
            <Text type="secondary">{t('rangeTotal')}</Text>
          </Table.Summary.Cell>
          <Table.Summary.Cell index={1} align="right">
            <Text strong>{fmtBytes(sumBytes(rows))}</Text>
          </Table.Summary.Cell>
        </Table.Summary.Row>
      )}
    />
  )
}
