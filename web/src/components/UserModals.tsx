import { useEffect, useState } from 'react'
import { Button, Checkbox, DatePicker, Form, Input, InputNumber, Modal, Popconfirm, Space, Spin, Switch, Tag, Typography } from 'antd'
import dayjs from 'dayjs'
import type { Dayjs } from 'dayjs'
import { api } from '../api'
import type { Node, NodeDetail, User } from '../api'
import { useI18n } from '../i18n'
import { useToast } from '../toast'
import { GB, bytesToGB } from '../format'
import { MODAL_WIDTH } from '../theme'

const { Text } = Typography

// 到期时间在库里是 'YYYY-MM-DD HH:MM:SS'，界面只取日期部分
const toExpireString = (value: Dayjs | null | undefined) =>
  value ? `${value.format('YYYY-MM-DD')} 00:00:00` : ''

export function CreateUserModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const { t } = useI18n()
  const toast = useToast()
  const [form] = Form.useForm()
  const [saving, setSaving] = useState(false)

  const submit = async (values: { name: string; limitGB: number; expire?: Dayjs; note?: string }) => {
    setSaving(true)
    try {
      await api.createUser({
        name: values.name,
        traffic_limit_bytes: Math.round((values.limitGB || 0) * GB),
        expire_at: toExpireString(values.expire),
        note: values.note ?? '',
      })
      toast.ok(t('userCreated'))
      form.resetFields()
      onClose()
      onCreated()
    } catch (error) {
      toast.err(error)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={open}
      title={t('newUser')}
      width={MODAL_WIDTH.sm}
      onCancel={onClose}
      okText={t('create')}
      cancelText={t('cancel')}
      confirmLoading={saving}
      onOk={form.submit}
      destroyOnHidden
    >
      <Form form={form} layout="vertical" onFinish={submit} initialValues={{ limitGB: 100 }} requiredMark={false}>
        <Form.Item name="name" label={t('username')} rules={[{ required: true }]}>
          <Input autoFocus />
        </Form.Item>
        <Form.Item name="limitGB" label={t('limitLabel')}>
          <InputNumber min={0} style={{ width: '100%' }} />
        </Form.Item>
        <Form.Item name="expire" label={t('expireLabel')}>
          <DatePicker style={{ width: '100%' }} />
        </Form.Item>
        <Form.Item name="note" label={t('note')}>
          <Input />
        </Form.Item>
      </Form>
    </Modal>
  )
}

export function EditUserModal({
  user,
  nodes,
  onClose,
  onSaved,
}: {
  user: User | null
  nodes: Node[]
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useI18n()
  const toast = useToast()
  const [form] = Form.useForm()
  const [saving, setSaving] = useState(false)
  const [loadingAccess, setLoadingAccess] = useState(false)
  const [accessReady, setAccessReady] = useState(false)
  const [nodeDetails, setNodeDetails] = useState<NodeDetail[]>([])

  useEffect(() => {
    if (!user) return
    let active = true
    setLoadingAccess(true)
    setAccessReady(false)
    setNodeDetails([])
    form.resetFields()
    // 节点列表不含入站；授权与全部线路一起加载，失败时不允许误存成空授权。
    Promise.all([api.userAccess(user.id), Promise.all(nodes.map((node) => api.node(node.id)))])
      .then(([accessible, details]) => {
        if (!active) return
        setNodeDetails(details)
        form.setFieldsValue({
          enabled: user.enabled,
          note: user.note,
          inboundIds: (accessible ?? []).map((inbound) => inbound.id),
          limitGB: bytesToGB(user.traffic_limit_bytes),
          expire: user.expire_at ? dayjs(user.expire_at.split(' ')[0]) : null,
          resetDay: user.traffic_reset_day || 1,
        })
        setAccessReady(true)
      })
      .catch((error) => {
        if (active) toast.err(error)
      })
      .finally(() => {
        if (active) setLoadingAccess(false)
      })
    return () => { active = false }
  }, [user, nodes, form, toast])

  const submit = async (values: {
    enabled: boolean
    note?: string
    inboundIds: number[]
    limitGB: number
    expire?: Dayjs
    resetDay: number
  }) => {
    if (!user || !accessReady || loadingAccess) return
    setSaving(true)
    try {
      const result = await api.updateUser(user.id, {
        enabled: values.enabled,
        inbound_ids: values.inboundIds ?? [],
        traffic_limit_bytes: Math.round((values.limitGB || 0) * GB),
        expire_at: toExpireString(values.expire),
        traffic_reset_day: values.resetDay,
        note: values.note ?? '',
      })
      const pushed = (result.sync ?? []).filter((item) => item.status === 'pushed').length
      toast.ok(t('savedSynced', { n: pushed }))
      onClose()
      onSaved()
    } catch (error) {
      toast.err(error)
    } finally {
      setSaving(false)
    }
  }

  const resetSubToken = async () => {
    if (!user) return
    try {
      await api.resetSubToken(user.id)
      toast.ok(t('tokenReset'))
      onClose()
      onSaved()
    } catch (error) {
      toast.err(error)
    }
  }

  return (
    <Modal
      open={user !== null}
      title={`${t('editUser')}：${user?.name ?? ''}`}
      width={MODAL_WIDTH.md}
      onCancel={onClose}
      destroyOnHidden
      footer={
        <Space style={{ width: '100%', justifyContent: 'space-between' }}>
          <Popconfirm
            title={t('resetTokenConfirm')}
            okText={t('confirm')}
            cancelText={t('cancel')}
            okButtonProps={{ danger: true }}
            onConfirm={resetSubToken}
          >
            <Button danger>{t('resetSubToken')}</Button>
          </Popconfirm>
          <Space>
            <Button onClick={onClose}>{t('cancel')}</Button>
            <Button type="primary" loading={saving} disabled={!accessReady || loadingAccess} onClick={form.submit}>
              {t('saveSync')}
            </Button>
          </Space>
        </Space>
      }
    >
      <Form form={form} layout="vertical" onFinish={submit} disabled={!accessReady || loadingAccess || saving} requiredMark={false}>
        <Form.Item name="enabled" label={t('userEnabled')} valuePropName="checked" extra={t('syncHint')}>
          <Switch />
        </Form.Item>
        <Form.Item name="note" label={t('note')}>
          <Input />
        </Form.Item>
        <Form.Item
          name="inboundIds"
          extra={t('inboundAccessHint')}
          label={
            <Space>
              {t('inboundAccess')}
              {loadingAccess && <Spin size="small" />}
              <Button
                type="link"
                size="small"
                onClick={() => form.setFieldValue('inboundIds', nodeDetails.flatMap((node) => (node.inbounds ?? []).map((inbound) => inbound.id)))}
              >
                {t('selectAll')}
              </Button>
              <Button type="link" size="small" onClick={() => form.setFieldValue('inboundIds', [])}>
                {t('none')}
              </Button>
            </Space>
          }
        >
          <Checkbox.Group style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            {nodeDetails.map((node) => (
              <div key={node.id}>
                <Text strong>{node.name}</Text>
                <Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
                  {node.domain || node.host}
                </Text>
                <Space direction="vertical" size={6} style={{ display: 'flex', marginTop: 6 }}>
                  {(node.inbounds ?? []).map((inbound) => (
                    <Checkbox key={inbound.id} value={inbound.id}>
                      {inbound.name || inbound.tag}
                      <Text type="secondary" style={{ margin: '0 8px', fontSize: 12 }}>
                        {inbound.protocol} · {inbound.port} · {inbound.tag}
                      </Text>
                      <Tag color={inbound.outbound ? 'purple' : 'default'}>
                        {t('egress')}: {inbound.outbound
                          ? `SOCKS5 · ${inbound.outbound.server}:${inbound.outbound.server_port}`
                          : t('egressDirect')}
                      </Tag>
                      {!inbound.enabled && <Tag>{t('disabled')}</Tag>}
                    </Checkbox>
                  ))}
                  {!node.inbounds?.length && <Text type="secondary">{t('noInboundAccessOptions')}</Text>}
                </Space>
              </div>
            ))}
          </Checkbox.Group>
        </Form.Item>
        <Form.Item name="limitGB" label={t('limitLabel')}>
          <InputNumber min={0} style={{ width: '100%' }} />
        </Form.Item>
        <Form.Item name="expire" label={t('expireLabel')}>
          <DatePicker style={{ width: '100%' }} />
        </Form.Item>
        <Form.Item name="resetDay" label={t('resetDayLabel')}>
          <InputNumber min={1} max={28} style={{ width: '100%' }} />
        </Form.Item>
      </Form>
    </Modal>
  )
}
