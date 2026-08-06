import { useEffect, useState } from 'react'
import { Button, Checkbox, DatePicker, Form, Input, InputNumber, Modal, Popconfirm, Space, Switch, Typography } from 'antd'
import dayjs from 'dayjs'
import type { Dayjs } from 'dayjs'
import { api } from '../api'
import type { Node, User } from '../api'
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

  useEffect(() => {
    if (!user) return
    setLoadingAccess(true)
    api
      .userAccess(user.id)
      .then((accessible) => {
        form.setFieldsValue({
          enabled: user.enabled,
          note: user.note,
          nodeIds: (accessible ?? []).map((node) => node.id),
          limitGB: bytesToGB(user.traffic_limit_bytes),
          expire: user.expire_at ? dayjs(user.expire_at.split(' ')[0]) : null,
          resetDay: user.traffic_reset_day || 1,
        })
      })
      .catch(toast.err)
      .finally(() => setLoadingAccess(false))
  }, [user, form, toast])

  const submit = async (values: {
    enabled: boolean
    note?: string
    nodeIds: number[]
    limitGB: number
    expire?: Dayjs
    resetDay: number
  }) => {
    if (!user) return
    setSaving(true)
    try {
      const result = await api.updateUser(user.id, {
        enabled: values.enabled,
        node_ids: values.nodeIds ?? [],
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
            <Button type="primary" loading={saving} onClick={form.submit}>
              {t('saveSync')}
            </Button>
          </Space>
        </Space>
      }
    >
      <Form form={form} layout="vertical" onFinish={submit} disabled={loadingAccess} requiredMark={false}>
        <Form.Item name="enabled" label={t('userEnabled')} valuePropName="checked" extra={t('syncHint')}>
          <Switch />
        </Form.Item>
        <Form.Item name="note" label={t('note')}>
          <Input />
        </Form.Item>
        <Form.Item
          name="nodeIds"
          label={
            <Space>
              {t('nodeAccess')}
              <Button
                type="link"
                size="small"
                onClick={() => form.setFieldValue('nodeIds', nodes.map((node) => node.id))}
              >
                {t('selectAll')}
              </Button>
              <Button type="link" size="small" onClick={() => form.setFieldValue('nodeIds', [])}>
                {t('none')}
              </Button>
            </Space>
          }
        >
          <Checkbox.Group style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            {nodes.map((node) => (
              <Checkbox key={node.id} value={node.id}>
                {node.name}
                <Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
                  {node.domain || node.host}
                </Text>
              </Checkbox>
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
