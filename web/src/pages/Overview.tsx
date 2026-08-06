import { useEffect, useState } from 'react'
import { Card, Col, List, Row, Skeleton, Space, Tag, Typography } from 'antd'
import { api } from '../api'
import type { Node, UsageRow, User } from '../api'
import { useI18n } from '../i18n'
import { useToast } from '../toast'
import { PageHeader, StatTile, EmptyState, Mono } from '../ui'
import { SEMANTIC_HEX, COLOR_PRIMARY } from '../theme'
import { fmtBytes } from '../format'
import { shiftDay } from '../usage'

const { Text } = Typography

export default function Overview() {
  const { t } = useI18n()
  const toast = useToast()
  const [loading, setLoading] = useState(true)
  const [users, setUsers] = useState<User[]>([])
  const [nodes, setNodes] = useState<Node[]>([])
  const [todayByUser, setTodayByUser] = useState<UsageRow[]>([])
  const [weekTotal, setWeekTotal] = useState<UsageRow | null>(null)

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const meta = await api.statsMeta()
        const [userList, nodeList, today, week] = await Promise.all([
          api.users(),
          api.nodes(),
          api.usage({ from: meta.today, to: meta.today, group: ['user'] }),
          api.usage({ from: shiftDay(meta.today, -6), to: meta.today }),
        ])
        if (cancelled) return
        setUsers(userList ?? [])
        setNodes(nodeList ?? [])
        setTodayByUser(today)
        setWeekTotal(week[0] ?? null)
      } catch (error) {
        if (!cancelled) toast.err(error)
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    load()
    return () => {
      cancelled = true
    }
  }, [toast])

  const todayTotal = todayByUser.reduce((sum, row) => sum + row.up + row.down, 0)
  const weekBytes = weekTotal ? weekTotal.up + weekTotal.down : 0

  if (loading) return <Skeleton active />

  return (
    <>
      <PageHeader title={t('overview')} />
      <Row gutter={[14, 14]} style={{ marginBottom: 16 }}>
        <Col xs={24} sm={12} lg={6}>
          <StatTile
            label={t('users')}
            value={users.length}
            foot={`${users.filter((u) => u.enabled).length} ${t('enabled')}`}
            accent={COLOR_PRIMARY}
          />
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <StatTile
            label={t('nodes')}
            value={nodes.length}
            foot={`${nodes.filter((n) => n.enabled).length} ${t('enabled')}`}
            accent={SEMANTIC_HEX.down}
          />
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <StatTile
            label={t('usageToday')}
            value={fmtBytes(todayTotal)}
            foot={`${todayByUser.length} ${t('activeUsers')}`}
            accent={SEMANTIC_HEX.warn}
          />
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <StatTile
            label={t('usageLast7')}
            value={fmtBytes(weekBytes)}
            foot={`↑ ${fmtBytes(weekTotal?.up)} ↓ ${fmtBytes(weekTotal?.down)}`}
            accent={SEMANTIC_HEX.fail}
          />
        </Col>
      </Row>

      <Row gutter={[14, 14]}>
        <Col xs={24} lg={12}>
          <Card size="small" title={t('usageToday')}>
            <List
              size="small"
              dataSource={todayByUser}
              locale={{ emptyText: <EmptyState description={t('noData')} /> }}
              renderItem={(row) => (
                <List.Item>
                  <Text strong>{row.user}</Text>
                  <Mono>{fmtBytes(row.up + row.down)}</Mono>
                </List.Item>
              )}
            />
          </Card>
        </Col>
        <Col xs={24} lg={12}>
          <Card size="small" title={t('nodes')}>
            <List
              size="small"
              dataSource={nodes}
              locale={{ emptyText: <EmptyState description={t('noData')} /> }}
              renderItem={(node) => (
                <List.Item>
                  <Space>
                    <Tag color={node.enabled ? 'green' : 'default'}>{node.enabled ? t('enabled') : t('disabled')}</Tag>
                    <Text strong>{node.name}</Text>
                  </Space>
                  <Mono>{node.domain || node.host}</Mono>
                </List.Item>
              )}
            />
          </Card>
        </Col>
      </Row>
    </>
  )
}
