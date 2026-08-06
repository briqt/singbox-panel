import { useCallback, useEffect, useState } from 'react'
import { Badge, Button, Popconfirm, Progress, Space, Table, Tooltip, Typography } from 'antd'
import { PlusOutlined } from '@ant-design/icons'
import { api } from '../api'
import type { Node, User } from '../api'
import { useI18n } from '../i18n'
import { useToast } from '../toast'
import { EmptyState, Mono, PageHeader } from '../ui'
import { fmtBytes } from '../format'
import { SEMANTIC_HEX } from '../theme'
import { CreateUserModal, EditUserModal } from '../components/UserModals'

const { Text } = Typography

export default function Users() {
  const { t } = useI18n()
  const toast = useToast()
  const [users, setUsers] = useState<User[]>([])
  const [nodes, setNodes] = useState<Node[]>([])
  const [loading, setLoading] = useState(true)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<User | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [userList, nodeList] = await Promise.all([api.users(), api.nodes()])
      setUsers(userList ?? [])
      setNodes(nodeList ?? [])
    } catch (error) {
      toast.err(error)
    } finally {
      setLoading(false)
    }
  }, [toast])

  useEffect(() => {
    load()
  }, [load])

  const copySubLink = async (user: User) => {
    await navigator.clipboard.writeText(`${location.origin}/sub/${user.sub_token}`)
    toast.ok(t('copied'))
  }

  const resetTraffic = async (user: User) => {
    try {
      await api.resetTraffic(user.id)
      toast.ok(t('trafficReset'))
      load()
    } catch (error) {
      toast.err(error)
    }
  }

  const remove = async (user: User) => {
    try {
      await api.deleteUser(user.id)
      toast.ok(t('userDeleted'))
      load()
    } catch (error) {
      toast.err(error)
    }
  }

  return (
    <>
      <PageHeader
        title={t('users')}
        extra={
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreating(true)}>
            {t('newUser')}
          </Button>
        }
      />
      <Table
        size="small"
        rowKey="id"
        loading={loading}
        dataSource={users}
        pagination={false}
        locale={{ emptyText: <EmptyState description={t('noUsers')} /> }}
        columns={[
          {
            title: t('user'),
            dataIndex: 'name',
            render: (name: string, user) => (
              <Space direction="vertical" size={0}>
                <Badge status={user.enabled ? 'success' : 'default'} text={<Text strong>{name}</Text>} />
                {user.note && (
                  <Text type="secondary" style={{ fontSize: 12, marginLeft: 14 }}>
                    {user.note}
                  </Text>
                )}
              </Space>
            ),
          },
          {
            title: t('traffic'),
            width: 260,
            render: (_, user) => {
              const percent = user.traffic_limit_bytes
                ? Math.min(100, Math.round((user.traffic_used_bytes / user.traffic_limit_bytes) * 100))
                : 0
              return (
                <Space direction="vertical" size={0} style={{ width: '100%' }}>
                  <Mono>
                    {fmtBytes(user.traffic_used_bytes)} /{' '}
                    {user.traffic_limit_bytes ? fmtBytes(user.traffic_limit_bytes) : t('unlimited')}
                  </Mono>
                  {user.traffic_limit_bytes > 0 && (
                    <Progress
                      percent={percent}
                      size="small"
                      showInfo={false}
                      strokeColor={percent > 80 ? SEMANTIC_HEX.fail : undefined}
                    />
                  )}
                </Space>
              )
            },
          },
          {
            title: t('expires'),
            dataIndex: 'expire_at',
            width: 140,
            render: (value: string) => <Mono>{value ? value.split(' ')[0] : t('never')}</Mono>,
          },
          {
            title: t('subscription'),
            width: 110,
            render: (_, user) => (
              <Button type="link" size="small" style={{ padding: 0 }} onClick={() => copySubLink(user)}>
                {t('copyLink')}
              </Button>
            ),
          },
          {
            title: t('actions'),
            width: 190,
            align: 'right',
            render: (_, user) => (
              <Space size={4}>
                <Button size="small" type="link" onClick={() => setEditing(user)}>
                  {t('edit')}
                </Button>
                <Popconfirm
                  title={t('resetTrafficConfirm', { name: user.name })}
                  okText={t('confirm')}
                  cancelText={t('cancel')}
                  onConfirm={() => resetTraffic(user)}
                >
                  <Tooltip title={t('resetTrafficConfirm', { name: user.name })}>
                    <Button size="small" type="link">
                      {t('reset')}
                    </Button>
                  </Tooltip>
                </Popconfirm>
                <Popconfirm
                  title={t('deleteUserConfirm', { name: user.name })}
                  okText={t('del')}
                  cancelText={t('cancel')}
                  okButtonProps={{ danger: true }}
                  onConfirm={() => remove(user)}
                >
                  <Button size="small" type="link" danger>
                    {t('del')}
                  </Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />

      <CreateUserModal open={creating} onClose={() => setCreating(false)} onCreated={load} />
      <EditUserModal user={editing} nodes={nodes} onClose={() => setEditing(null)} onSaved={load} />
    </>
  )
}
