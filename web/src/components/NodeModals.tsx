import { useCallback, useEffect, useState } from 'react'
import { Alert, Checkbox, Form, Input, InputNumber, Modal, Select, Typography } from 'antd'
import { api } from '../api'
import type { Node, NodeDetail, SetupAssessment } from '../api'
import { useI18n } from '../i18n'
import { useToast } from '../toast'
import { MODAL_WIDTH } from '../theme'

const { Text } = Typography
const { TextArea } = Input

const PROTOCOLS = [
  { value: 'hysteria2', label: 'Hysteria2', hintKey: 'needsDomain' },
  { value: 'vless-reality', label: 'VLESS Reality', hintKey: 'noDomainNeeded' },
  { value: 'vless-httpupgrade', label: 'VLESS HTTPUpgrade', hintKey: 'forCDN' },
] as const

export function CreateNodeModal({
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

  const submit = async (values: { name: string; host: string; domain?: string; port: number; ssh_user: string }) => {
    setSaving(true)
    try {
      await api.createNode({
        name: values.name,
        host: values.host,
        domain: values.domain ?? '',
        port: values.port || 22,
        ssh_user: values.ssh_user || 'root',
      })
      toast.ok(t('nodeCreated'))
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
      title={t('newNode')}
      width={MODAL_WIDTH.sm}
      onCancel={onClose}
      onOk={form.submit}
      okText={t('create')}
      cancelText={t('cancel')}
      confirmLoading={saving}
      destroyOnHidden
    >
      <Form
        form={form}
        layout="vertical"
        onFinish={submit}
        initialValues={{ port: 22, ssh_user: 'root' }}
        requiredMark={false}
      >
        <Form.Item name="name" label={t('nodeName')} rules={[{ required: true }]}>
          <Input autoFocus />
        </Form.Item>
        <Form.Item name="host" label={t('host')} rules={[{ required: true }]}>
          <Input placeholder="1.2.3.4" />
        </Form.Item>
        <Form.Item name="domain" label={t('domain')} extra={t('domainOptional')}>
          <Input />
        </Form.Item>
        <Form.Item name="port" label={t('sshPort')}>
          <InputNumber min={1} max={65535} style={{ width: '100%' }} />
        </Form.Item>
        <Form.Item name="ssh_user" label={t('sshUser')}>
          <Input />
        </Form.Item>
      </Form>
    </Modal>
  )
}

export function SetupSSHModal({ node, onClose }: { node: Node | null; onClose: () => void }) {
  const { t } = useI18n()
  const toast = useToast()
  const [form] = Form.useForm()
  const [saving, setSaving] = useState(false)

  const submit = async (values: { password: string }) => {
    if (!node) return
    setSaving(true)
    try {
      const result = await api.setupSSH(node.id, values.password)
      toast.ok(result.status === 'ready' ? t('sshConfigured') : result.status)
      form.resetFields()
      onClose()
    } catch (error) {
      toast.err(error)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={node !== null}
      title={`${t('setupSSH')} — ${node?.name ?? ''}`}
      width={MODAL_WIDTH.sm}
      onCancel={onClose}
      onOk={form.submit}
      okText={t('confirm')}
      cancelText={t('cancel')}
      confirmLoading={saving}
      destroyOnHidden
    >
      <Form form={form} layout="vertical" onFinish={submit} requiredMark={false}>
        <Form.Item name="password" label={t('rootPassword')} extra={t('sshHint')} rules={[{ required: true }]}>
          <Input.Password autoFocus />
        </Form.Item>
      </Form>
    </Modal>
  )
}

export function CertUploadModal({ node, onClose }: { node: Node | null; onClose: () => void }) {
  const { t } = useI18n()
  const toast = useToast()
  const [form] = Form.useForm()
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (node) form.setFieldsValue({ domain: node.domain, cert: '', key: '' })
  }, [node, form])

  const submit = async (values: { domain: string; cert: string; key: string }) => {
    if (!node) return
    setSaving(true)
    try {
      const result = await api.certUpload(node.id, values)
      toast.ok(result.cert_path ? `${t('certUploaded')}: ${result.cert_path}` : t('certUploaded'))
      onClose()
    } catch (error) {
      toast.err(error)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={node !== null}
      title={`${t('uploadCert')} — ${node?.name ?? ''}`}
      width={MODAL_WIDTH.md}
      onCancel={onClose}
      onOk={form.submit}
      okText={t('upload')}
      cancelText={t('cancel')}
      confirmLoading={saving}
      destroyOnHidden
    >
      <Form form={form} layout="vertical" onFinish={submit} requiredMark={false}>
        <Form.Item name="domain" label={t('domain')}>
          <Input />
        </Form.Item>
        <Form.Item name="cert" label={t('certPem')} rules={[{ required: true }]}>
          <TextArea rows={5} spellCheck={false} placeholder="-----BEGIN CERTIFICATE-----" />
        </Form.Item>
        <Form.Item name="key" label={t('keyPem')} rules={[{ required: true }]}>
          <TextArea rows={4} spellCheck={false} placeholder="-----BEGIN PRIVATE KEY-----" />
        </Form.Item>
      </Form>
    </Modal>
  )
}

// 一键配置：模式与域名任一变化都重新问后端要一次评估，
// 首次进入（节点尚无入站）才用评估建议覆盖协议勾选，避免抹掉既有部署。
export function AutoSetupModal({
  node,
  detail,
  onClose,
  onDone,
}: {
  node: Node | null
  detail?: NodeDetail
  onClose: () => void
  onDone: () => void
}) {
  const { t } = useI18n()
  const toast = useToast()
  const [mode, setMode] = useState('auto')
  const [domain, setDomain] = useState('')
  const [protocols, setProtocols] = useState<string[]>([])
  const [assessment, setAssessment] = useState<SetupAssessment | null>(null)
  const [assessError, setAssessError] = useState('')
  const [assessing, setAssessing] = useState(false)
  const [running, setRunning] = useState(false)

  const existing = [...new Set((detail?.inbounds ?? []).map((inbound) => inbound.protocol))]
  const hasExisting = existing.length > 0

  useEffect(() => {
    if (!node) return
    setDomain(node.domain ?? '')
    setMode(
      existing.includes('vless-httpupgrade')
        ? 'cdn'
        : existing.includes('hysteria2')
          ? 'direct'
          : hasExisting
            ? 'reality'
            : 'auto',
    )
    setProtocols(hasExisting ? existing : node.domain ? ['hysteria2', 'vless-reality'] : ['vless-reality'])
    setAssessment(null)
    setAssessError('')
    // 仅在打开弹窗（切换节点）时初始化，之后交给用户操作
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [node])

  const assess = useCallback(
    async (applySuggestion: boolean) => {
      if (!node) return
      setAssessing(true)
      setAssessError('')
      try {
        const response = await api.setupAssessment(node.id, mode, domain)
        const result = (response.assessment ?? (response as SetupAssessment)) || null
        setAssessment(result)
        setAssessError(response.error ?? '')
        if (applySuggestion && !hasExisting && result?.suggested_protocols?.length) {
          setProtocols([...result.suggested_protocols])
        }
      } catch (error) {
        setAssessment(null)
        setAssessError(error instanceof Error ? error.message : String(error))
      } finally {
        setAssessing(false)
      }
    },
    [node, mode, domain, hasExisting],
  )

  useEffect(() => {
    if (node) assess(!hasExisting)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [node, mode, domain])

  const run = async () => {
    if (!node) return
    if (mode === 'auto' && assessment?.requires_confirmation) {
      toast.err(new Error(t('autoSetupNeedMode')))
      return
    }
    if (assessError) {
      toast.err(new Error(assessError))
      return
    }
    setRunning(true)
    try {
      const result = await api.autoSetup(node.id, { domain, mode, protocols })
      const changed = (result.inbounds ?? []).filter(
        (inbound) => inbound.status === 'ok' || inbound.status === 'updated',
      ).length
      toast.ok(t('setupDone', { n: changed }))
      onClose()
      onDone()
    } catch (error) {
      toast.err(error)
    } finally {
      setRunning(false)
    }
  }

  const dnsText = assessment
    ? assessment.dns_status === 'not_required'
      ? t('dnsNotReq')
      : assessment.direct_to_node
        ? t('dnsDirect')
        : t('dnsIndirect')
    : ''

  return (
    <Modal
      open={node !== null}
      title={`${t('autoSetup')} — ${node?.name ?? ''}`}
      width={MODAL_WIDTH.md}
      onCancel={onClose}
      onOk={run}
      okText={t('startSetup')}
      cancelText={t('cancel')}
      confirmLoading={running}
      destroyOnHidden
    >
      <Form layout="vertical" requiredMark={false}>
        <Form.Item label={t('deployMode')}>
          <Select
            value={mode}
            onChange={setMode}
            options={[
              { value: 'auto', label: t('modeAuto') },
              { value: 'direct', label: t('modeDirect') },
              { value: 'cdn', label: t('modeCdn') },
              { value: 'reality', label: t('modeReality') },
            ]}
          />
        </Form.Item>
        <Form.Item label={t('domain')} extra={t('autoSetupDomainHint')}>
          <Input value={domain} onChange={(event) => setDomain(event.target.value)} onBlur={() => assess(false)} />
        </Form.Item>
        {(assessment || assessError || assessing) && (
          <Alert
            style={{ marginBottom: 16 }}
            type={assessError || assessment?.requires_confirmation ? 'warning' : 'success'}
            message={
              assessing
                ? t('assessing')
                : assessError || (
                    <span>
                      {dnsText}
                      {assessment?.dns_records?.length ? ` (${assessment.dns_records.join(', ')})` : ''}
                    </span>
                  )
            }
            description={
              !assessing && assessment?.warnings?.length ? (
                <ul style={{ margin: 0, paddingLeft: 18 }}>
                  {assessment.warnings.map((warning) => (
                    <li key={warning}>{warning}</li>
                  ))}
                </ul>
              ) : undefined
            }
          />
        )}
        <Form.Item label={t('protocols')} style={{ marginBottom: 0 }}>
          <Checkbox.Group
            value={protocols}
            onChange={(value) => setProtocols(value as string[])}
            style={{ display: 'flex', flexDirection: 'column', gap: 6 }}
          >
            {PROTOCOLS.map((protocol) => (
              <Checkbox key={protocol.value} value={protocol.value}>
                {protocol.label}
                <Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
                  {t(protocol.hintKey)}
                </Text>
              </Checkbox>
            ))}
          </Checkbox.Group>
        </Form.Item>
      </Form>
    </Modal>
  )
}

// 配置查看：预览（本地生成）与线上配置共用，只读
export function ConfigModal({
  title,
  content,
  onClose,
}: {
  title: string
  content: string | null
  onClose: () => void
}) {
  const { t } = useI18n()
  return (
    <Modal
      open={content !== null}
      title={title}
      width={MODAL_WIDTH.xl}
      onCancel={onClose}
      footer={null}
      destroyOnHidden
    >
      <TextArea
        value={content ?? ''}
        readOnly
        autoSize={{ minRows: 18, maxRows: 26 }}
        style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: 12 }}
        aria-label={t('rawConfig')}
      />
    </Modal>
  )
}
