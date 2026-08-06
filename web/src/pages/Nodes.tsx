import { useCallback, useEffect, useState } from 'react'
import { Badge, Button, Collapse, Popconfirm, Space, Spin, Table, Tag, Tooltip, Typography } from 'antd'
import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  DeleteOutlined,
  PlusOutlined,
  ReloadOutlined,
} from '@ant-design/icons'
import { api } from '../api'
import type { Inbound, Node, NodeDetail, NodeStatus } from '../api'
import { useI18n } from '../i18n'
import { useToast } from '../toast'
import { EmptyState, Mono, PageHeader } from '../ui'
import { NODE_HEALTH_COLOR } from '../theme'
import { fmtBytes, fmtClock, fmtUptime } from '../format'
import { healthLabel, healthState } from '../nodeHealth'
import { AutoSetupModal, CertUploadModal, ConfigModal, CreateNodeModal, SetupSSHModal } from '../components/NodeModals'

const { Text } = Typography

// 同时检测的节点数：每次检测都是一条 SSH 会话，并发太高会把面板机的连接数吃满
const STATUS_CONCURRENCY = 3

export default function Nodes() {
  const { t } = useI18n()
  const toast = useToast()
  const [nodes, setNodes] = useState<Node[]>([])
  const [details, setDetails] = useState<Record<number, NodeDetail>>({})
  const [statuses, setStatuses] = useState<Record<number, NodeStatus>>({})
  const [checking, setChecking] = useState<Record<number, boolean>>({})
  const [refreshing, setRefreshing] = useState(false)
  const [loading, setLoading] = useState(true)
  const [expanded, setExpanded] = useState<number[]>([])
  const [creating, setCreating] = useState(false)
  const [sshNode, setSSHNode] = useState<Node | null>(null)
  const [certNode, setCertNode] = useState<Node | null>(null)
  const [setupNode, setSetupNode] = useState<Node | null>(null)
  const [config, setConfig] = useState<{ title: string; content: string } | null>(null)

  const loadNodes = useCallback(async () => {
    setLoading(true)
    try {
      setNodes((await api.nodes()) ?? [])
    } catch (error) {
      toast.err(error)
    } finally {
      setLoading(false)
    }
  }, [toast])

  useEffect(() => {
    loadNodes()
  }, [loadNodes])

  const loadDetail = useCallback(async (nodeId: number) => {
    const detail = await api.node(nodeId)
    setDetails((current) => ({ ...current, [nodeId]: detail }))
  }, [])

  const checkStatus = useCallback(
    async (node: Node) => {
      setChecking((current) => ({ ...current, [node.id]: true }))
      try {
        const status = await api.nodeStatus(node.id)
        setStatuses((current) => ({ ...current, [node.id]: { ...status, checked_at: Date.now() } }))
      } catch (error) {
        setStatuses((current) => ({
          ...current,
          [node.id]: {
            node: node.name,
            reachable: false,
            error: error instanceof Error ? error.message : String(error),
            checked_at: Date.now(),
          },
        }))
      } finally {
        setChecking((current) => ({ ...current, [node.id]: false }))
      }
    },
    [],
  )

  // 打开节点页即自动体检一遍，避免面板显示的是上次会话的旧状态
  const checkAll = useCallback(
    async (notify: boolean) => {
      if (!nodes.length) return
      setRefreshing(true)
      const queue = [...nodes]
      const worker = async () => {
        while (queue.length) {
          const node = queue.shift()
          if (node) await checkStatus(node)
        }
      }
      await Promise.all(Array.from({ length: Math.min(STATUS_CONCURRENCY, queue.length) }, worker))
      setRefreshing(false)
      if (notify) {
        setStatuses((current) => {
          const unhealthy = nodes.filter((node) => healthState(current[node.id]) !== 'healthy').length
          if (unhealthy) toast.err(new Error(t('statusNeedAttention', { n: unhealthy })))
          else toast.ok(t('statusAllHealthy'))
          return current
        })
      }
    },
    [nodes, checkStatus, toast, t],
  )

  useEffect(() => {
    if (nodes.length) checkAll(false)
    // 节点列表变化时重新体检；notify 只在手动刷新时给
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodes])

  const move = async (node: Node, delta: number) => {
    const index = nodes.findIndex((item) => item.id === node.id)
    const target = index + delta
    if (target < 0 || target >= nodes.length) return
    const reordered = [...nodes]
    ;[reordered[index], reordered[target]] = [reordered[target], reordered[index]]
    setNodes(reordered)
    try {
      await api.reorderNodes(reordered.map((item, position) => ({ id: item.id, sort_order: position })))
    } catch (error) {
      toast.err(error)
      loadNodes()
    }
  }

  const moveInbound = async (nodeId: number, inbound: Inbound, delta: number) => {
    const inbounds = details[nodeId]?.inbounds ?? []
    const index = inbounds.findIndex((item) => item.id === inbound.id)
    const target = index + delta
    if (target < 0 || target >= inbounds.length) return
    const reordered = [...inbounds]
    ;[reordered[index], reordered[target]] = [reordered[target], reordered[index]]
    try {
      await api.reorderInbounds(
        nodeId,
        reordered.map((item, position) => ({ id: item.id, sort_order: position })),
      )
      await loadDetail(nodeId)
    } catch (error) {
      toast.err(error)
    }
  }

  const removeInbound = async (nodeId: number, inbound: Inbound) => {
    try {
      await api.deleteInbound(inbound.id)
      toast.ok(t('inboundDeleted'))
      await loadDetail(nodeId)
    } catch (error) {
      toast.err(error)
    }
  }

  const removeNode = async (node: Node) => {
    try {
      await api.deleteNode(node.id)
      toast.ok(t('nodeDeleted'))
      loadNodes()
    } catch (error) {
      toast.err(error)
    }
  }

  const install = async (node: Node) => {
    try {
      const result = await api.install(node.id)
      toast.ok(t('installed', { name: node.name, version: result.version }))
      checkStatus(node)
    } catch (error) {
      toast.err(error)
    }
  }

  const push = async (node: Node) => {
    try {
      await api.push(node.id)
      toast.ok(t('pushed', { name: node.name }))
    } catch (error) {
      toast.err(error)
    }
  }

  const pushAll = async () => {
    try {
      const results = await api.pushAll()
      const failed = results.filter((result) => result.status !== 'pushed')
      const pushed = results.length - failed.length
      if (failed.length) {
        toast.err(
          new Error(t('pushedPartial', { n: pushed, failed: failed.map((item) => item.node || item.node_id).join(', ') })),
        )
      } else {
        toast.ok(t('pushedAll', { n: pushed }))
      }
    } catch (error) {
      toast.err(error)
    }
  }

  const showConfig = async (node: Node, kind: 'preview' | 'raw') => {
    setConfig({ title: `${kind === 'preview' ? t('preview') : t('rawConfig')} — ${node.name}`, content: '…' })
    try {
      const result = kind === 'preview' ? await api.generate(node.id) : await api.rawConfig(node.id)
      const text = typeof result === 'string' ? result : JSON.stringify(result, null, 2)
      setConfig({ title: `${kind === 'preview' ? t('preview') : t('rawConfig')} — ${node.name}`, content: text })
    } catch (error) {
      setConfig({ title: node.name, content: `// ${error instanceof Error ? error.message : String(error)}` })
    }
  }

  const healthySummary = nodes.length
    ? refreshing
      ? t('refreshingStatus')
      : t('healthySummary', {
          healthy: nodes.filter((node) => healthState(statuses[node.id]) === 'healthy').length,
          total: nodes.length,
        })
    : undefined

  return (
    <>
      <PageHeader
        title={t('nodes')}
        description={healthySummary}
        extra={
          <Space>
            <Button icon={<ReloadOutlined />} loading={refreshing} onClick={() => checkAll(true)}>
              {t('refreshStatus')}
            </Button>
            <Popconfirm
              title={t('pushAllConfirm')}
              okText={t('confirm')}
              cancelText={t('cancel')}
              onConfirm={pushAll}
            >
              <Button>{t('pushAll')}</Button>
            </Popconfirm>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreating(true)}>
              {t('newNode')}
            </Button>
          </Space>
        }
      />

      {loading ? (
        <Spin />
      ) : nodes.length === 0 ? (
        <EmptyState description={t('noNodes')} />
      ) : (
        <Collapse
          activeKey={expanded.map(String)}
          onChange={(keys) => {
            const ids = (keys as string[]).map(Number)
            setExpanded(ids)
            ids.filter((id) => !details[id]).forEach((id) => loadDetail(id).catch(toast.err))
          }}
          items={nodes.map((node, index) => {
            const status = statuses[node.id]
            const state = healthState(status)
            return {
              key: String(node.id),
              label: (
                <Space size={10} wrap>
                  <Badge status={node.enabled ? 'success' : 'default'} />
                  <Text strong>{node.name}</Text>
                  {node.domain && <Mono>{node.domain}</Mono>}
                  <Mono>{node.host}</Mono>
                </Space>
              ),
              extra: (
                <Space size={6} onClick={(event) => event.stopPropagation()}>
                  {status?.version && <Mono>{status.version}</Mono>}
                  {checking[node.id] ? (
                    <Tag>{t('checking')}</Tag>
                  ) : (
                    <Tag color={NODE_HEALTH_COLOR[state]}>{healthLabel(status, t)}</Tag>
                  )}
                  <Button
                    size="small"
                    type="text"
                    icon={<ArrowUpOutlined />}
                    disabled={index === 0}
                    onClick={() => move(node, -1)}
                  />
                  <Button
                    size="small"
                    type="text"
                    icon={<ArrowDownOutlined />}
                    disabled={index === nodes.length - 1}
                    onClick={() => move(node, 1)}
                  />
                </Space>
              ),
              children: (
                <NodeBody
                  node={node}
                  detail={details[node.id]}
                  status={status}
                  checking={!!checking[node.id]}
                  onCheck={() => checkStatus(node)}
                  onSSH={() => setSSHNode(node)}
                  onInstall={() => install(node)}
                  onAutoSetup={() => setSetupNode(node)}
                  onCert={() => setCertNode(node)}
                  onPush={() => push(node)}
                  onPreview={() => showConfig(node, 'preview')}
                  onRaw={() => showConfig(node, 'raw')}
                  onDelete={() => removeNode(node)}
                  onMoveInbound={(inbound, delta) => moveInbound(node.id, inbound, delta)}
                  onDeleteInbound={(inbound) => removeInbound(node.id, inbound)}
                />
              ),
            }
          })}
        />
      )}

      <CreateNodeModal open={creating} onClose={() => setCreating(false)} onCreated={loadNodes} />
      <SetupSSHModal node={sshNode} onClose={() => setSSHNode(null)} />
      <CertUploadModal node={certNode} onClose={() => setCertNode(null)} />
      <AutoSetupModal
        node={setupNode}
        detail={setupNode ? details[setupNode.id] : undefined}
        onClose={() => setSetupNode(null)}
        onDone={() => {
          loadNodes()
          if (setupNode) loadDetail(setupNode.id).catch(toast.err)
        }}
      />
      <ConfigModal title={config?.title ?? ''} content={config?.content ?? null} onClose={() => setConfig(null)} />
    </>
  )
}

function NodeBody({
  node,
  detail,
  status,
  checking,
  onCheck,
  onSSH,
  onInstall,
  onAutoSetup,
  onCert,
  onPush,
  onPreview,
  onRaw,
  onDelete,
  onMoveInbound,
  onDeleteInbound,
}: {
  node: Node
  detail?: NodeDetail
  status?: NodeStatus
  checking: boolean
  onCheck: () => void
  onSSH: () => void
  onInstall: () => void
  onAutoSetup: () => void
  onCert: () => void
  onPush: () => void
  onPreview: () => void
  onRaw: () => void
  onDelete: () => void
  onMoveInbound: (inbound: Inbound, delta: number) => void
  onDeleteInbound: (inbound: Inbound) => void
}) {
  const { t } = useI18n()
  const inbounds = detail?.inbounds ?? []
  const listeningOf = (inbound: Inbound) =>
    (status?.inbounds ?? []).find((item) => item.id === inbound.id)?.listening ?? null

  return (
    <>
      <Space wrap style={{ marginBottom: 12 }}>
        <Button size="small" icon={<ReloadOutlined />} loading={checking} onClick={onCheck}>
          {t('refreshStatus')}
        </Button>
        <Button size="small" onClick={onSSH}>
          {t('setupSSH')}
        </Button>
        <Popconfirm
          title={t('installConfirm', { name: node.name })}
          okText={t('confirm')}
          cancelText={t('cancel')}
          onConfirm={onInstall}
        >
          <Button size="small">{t('install')}</Button>
        </Popconfirm>
        <Button size="small" type="primary" ghost onClick={onAutoSetup}>
          {t('autoSetup')}
        </Button>
        <Button size="small" onClick={onCert}>
          {t('uploadCert')}
        </Button>
        <Popconfirm
          title={t('pushConfirm', { name: node.name })}
          okText={t('confirm')}
          cancelText={t('cancel')}
          onConfirm={onPush}
        >
          <Button size="small">{t('push')}</Button>
        </Popconfirm>
        <Button size="small" onClick={onPreview}>
          {t('preview')}
        </Button>
        <Button size="small" onClick={onRaw}>
          {t('rawConfig')}
        </Button>
        <Popconfirm
          title={t('deleteNodeConfirm', { name: node.name })}
          okText={t('del')}
          cancelText={t('cancel')}
          okButtonProps={{ danger: true }}
          onConfirm={onDelete}
        >
          <Button size="small" danger>
            {t('del')}
          </Button>
        </Popconfirm>
      </Space>

      {status?.reachable && (
        <Space size={20} wrap style={{ marginBottom: 12 }}>
          {status.mem_total ? (
            <Text type="secondary" style={{ fontSize: 12 }}>
              Mem <Mono>{fmtBytes(status.mem_total - (status.mem_available ?? 0))} / {fmtBytes(status.mem_total)}</Mono>
            </Text>
          ) : null}
          {status.disk_total ? (
            <Text type="secondary" style={{ fontSize: 12 }}>
              Disk <Mono>{fmtBytes(status.disk_used)} / {fmtBytes(status.disk_total)}</Mono>
            </Text>
          ) : null}
          {status.uptime ? (
            <Text type="secondary" style={{ fontSize: 12 }}>
              Up <Mono>{fmtUptime(status.uptime)}</Mono>
            </Text>
          ) : null}
          {status.checked_at ? (
            <Text type="secondary" style={{ fontSize: 12 }}>
              {t('checkedAt')} <Mono>{fmtClock(status.checked_at)}</Mono>
            </Text>
          ) : null}
        </Space>
      )}
      {status && !status.reachable && status.error && (
        <Text type="danger" style={{ display: 'block', marginBottom: 12, fontSize: 12 }}>
          {status.error}
        </Text>
      )}

      <Table
        size="small"
        rowKey="id"
        dataSource={inbounds}
        pagination={false}
        locale={{ emptyText: <EmptyState description={t('noInbounds')} /> }}
        columns={[
          {
            title: t('protocols'),
            dataIndex: 'protocol',
            render: (protocol: string) => <Tag color="blue">{protocol}</Tag>,
          },
          { title: 'Port', dataIndex: 'port', width: 100, render: (port: number) => <Mono>{port}</Mono> },
          {
            title: t('listening'),
            width: 130,
            render: (_, inbound) => {
              const listening = listeningOf(inbound)
              return (
                <Tag color={listening === true ? 'green' : listening === false ? 'red' : 'default'}>
                  {listening === true ? t('listening') : listening === false ? t('notListening') : t('unknown')}
                </Tag>
              )
            },
          },
          { title: 'Tag', dataIndex: 'tag', render: (tag: string) => <Mono>{tag}</Mono> },
          {
            title: t('actions'),
            width: 130,
            align: 'right',
            render: (_, inbound, index) => (
              <Space size={0}>
                <Button
                  size="small"
                  type="text"
                  icon={<ArrowUpOutlined />}
                  disabled={index === 0}
                  onClick={() => onMoveInbound(inbound, -1)}
                />
                <Button
                  size="small"
                  type="text"
                  icon={<ArrowDownOutlined />}
                  disabled={index === inbounds.length - 1}
                  onClick={() => onMoveInbound(inbound, 1)}
                />
                <Popconfirm
                  title={t('deleteInboundConfirm', { protocol: inbound.protocol })}
                  okText={t('del')}
                  cancelText={t('cancel')}
                  okButtonProps={{ danger: true }}
                  onConfirm={() => onDeleteInbound(inbound)}
                >
                  <Tooltip title={t('del')}>
                    <Button size="small" type="text" danger icon={<DeleteOutlined />} />
                  </Tooltip>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
    </>
  )
}
