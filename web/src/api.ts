// 类型与 fetch 封装：形状对齐后端真实返回（Go handler/*）。
// 错误约定：非 2xx 一律抛 Error，message = 后端 {"error": …}；401 触发登出回调。

export type Role = 'admin' | 'user'

export interface Session {
  token: string
  role: Role
  username: string
}

export interface User {
  id: number
  name: string
  uuid: string
  sub_token: string
  enabled: boolean
  traffic_limit_bytes: number
  traffic_used_bytes: number
  traffic_up_bytes: number
  traffic_down_bytes: number
  traffic_reset_day: number
  note: string
  expire_at: string
  created_at: string
  updated_at: string
}

export interface Node {
  id: number
  name: string
  host: string
  port: number
  domain: string
  ssh_user: string
  proxy_type: string
  config_path: string
  singbox_bin: string
  enabled: boolean
  sort_order: number
  created_at: string
}

export interface Inbound {
  id: number
  node_id: number
  tag: string
  protocol: string
  port: number
  enabled: boolean
  sort_order: number
}

export interface NodeDetail extends Node {
  inbounds: Inbound[]
}

// 节点上实际持有的证书，由后端从节点回读得到。vless-reality 借用真实站点的
// 证书、自己没有证书，其 InboundStatus 不带 cert 字段。
export interface CertStatus {
  domain: string
  path: string
  not_after?: string
  days_left: number
  expired: boolean
  error?: string
}

export interface InboundStatus {
  id: number
  protocol: string
  port: number
  network: string
  listening: boolean | null
  cert?: CertStatus
}

export interface CertRenewResult {
  domain: string
  protocol: string
  status: 'renewed' | 'fresh' | 'failed'
  before?: CertStatus
  after?: CertStatus
  details?: string
  acme_output?: string
}

export interface NodeStatus {
  node: string
  reachable: boolean
  installed?: boolean
  running?: boolean
  version?: string
  inbounds?: InboundStatus[]
  error?: string
  mem_total?: number
  mem_available?: number
  disk_total?: number
  disk_used?: number
  uptime?: number
  // 该节点所有 TLS 入站里最差的一张证书还剩几天；无 TLS 入站时后端不返回该字段。
  cert_days_left?: number
  checked_at?: number // 前端记录的本次检测时刻，非后端字段
}

export interface SetupAssessment {
  requested_mode: string
  effective_mode: string
  domain: string
  dns_status: string
  dns_records: string[]
  direct_to_node: boolean
  suggested_protocols: string[]
  requires_confirmation: boolean
  warnings: string[]
}

export interface SyncResult {
  node_id: number
  node: string
  status: string
  error?: string
}

export interface UserStat {
  id: number
  name: string
  enabled: boolean
  used_bytes: number
  up_bytes: number
  down_bytes: number
  limit_bytes: number
  expire_at: string
}

export interface NodeStat {
  id: number
  name: string
  enabled: boolean
  up_bytes: number
  down_bytes: number
}

// 统计口径元数据：时区、今天、可查询的最早一天（留存边界）
export interface StatsMeta {
  timezone: string
  today: string
  retention_from: string
  retention_months: number
}

// 用量行：只带请求分组的维度，未分组的维度缺省
export interface UsageRow {
  day?: string
  user_id?: number
  user?: string
  node_id?: number
  node?: string
  up: number
  down: number
}

export interface UsageQuery {
  from: string
  to: string
  group?: ('day' | 'user' | 'node')[]
  user_id?: number
  node_id?: number
}

export interface MeInfo {
  id: number
  name: string
  role: Role
  sub_token: string
  enabled: boolean
  traffic_used_bytes: number
  traffic_limit_bytes: number
  expire_at: string
}

const SESSION_KEY = 'panel_session'

export function loadSession(): Session | null {
  const raw = localStorage.getItem(SESSION_KEY)
  if (!raw) return null
  try {
    return JSON.parse(raw) as Session
  } catch {
    return null
  }
}

export function saveSession(session: Session) {
  localStorage.setItem(SESSION_KEY, JSON.stringify(session))
}

export function clearSession() {
  localStorage.removeItem(SESSION_KEY)
}

let onUnauthorized: () => void = () => {}

export function setUnauthorizedHandler(handler: () => void) {
  onUnauthorized = handler
}

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const session = loadSession()
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  if (session) headers.Authorization = `Bearer ${session.token}`

  const res = await fetch(url, { ...init, headers: { ...headers, ...(init?.headers as object) } })
  if (res.status === 401) {
    clearSession()
    onUnauthorized()
    throw new Error('unauthorized')
  }
  const text = await res.text()
  const body: unknown = text ? safeParse(text) : null
  if (!res.ok) {
    const detail =
      body && typeof body === 'object' && 'error' in body
        ? String((body as { error: unknown }).error)
        : `HTTP ${res.status}`
    throw new Error(detail)
  }
  return body as T
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return text
  }
}

const send = (method: string, body?: unknown): RequestInit => ({
  method,
  body: body === undefined ? undefined : JSON.stringify(body),
})

function usageParams(q: UsageQuery): string {
  const p = new URLSearchParams({ from: q.from, to: q.to })
  if (q.group?.length) p.set('group', q.group.join(','))
  if (q.user_id) p.set('user_id', String(q.user_id))
  if (q.node_id) p.set('node_id', String(q.node_id))
  return p.toString()
}

export const api = {
  login: (username: string, password: string) =>
    request<Session>('/api/login', send('POST', { username, password })),
  register: (username: string, password: string) =>
    request<{ id: number }>('/api/register', send('POST', { username, password })),

  me: () => request<MeInfo>('/api/me'),
  myNodes: () => request<Pick<Node, 'id' | 'name' | 'domain'>[]>('/api/me/nodes'),
  myUsage: (q: UsageQuery) => request<UsageRow[]>(`/api/me/usage?${usageParams(q)}`),

  users: () => request<User[]>('/api/users'),
  createUser: (body: { name: string; traffic_limit_bytes: number; expire_at?: string; note?: string }) =>
    request<User>('/api/users', send('POST', body)),
  updateUser: (
    id: number,
    body: {
      enabled?: boolean
      node_ids?: number[]
      traffic_limit_bytes?: number
      expire_at?: string
      traffic_reset_day?: number
      note?: string
    },
  ) => request<{ sync?: SyncResult[] }>(`/api/users/${id}`, send('PUT', body)),
  deleteUser: (id: number) => request<unknown>(`/api/users/${id}`, send('DELETE')),
  resetTraffic: (id: number) => request<unknown>(`/api/users/${id}/reset-traffic`, send('POST')),
  resetSubToken: (id: number) => request<unknown>(`/api/users/${id}/reset-sub-token`, send('POST')),
  userAccess: (id: number) => request<Node[] | null>(`/api/users/${id}/access`),

  nodes: () => request<Node[] | null>('/api/nodes'),
  node: (id: number) => request<NodeDetail>(`/api/nodes/${id}`),
  createNode: (body: { name: string; host: string; domain: string; port: number; ssh_user: string }) =>
    request<Node>('/api/nodes', send('POST', { ...body, proxy_type: 'singbox' })),
  deleteNode: (id: number) => request<unknown>(`/api/nodes/${id}`, send('DELETE')),
  reorderNodes: (items: { id: number; sort_order: number }[]) =>
    request<unknown>('/api/nodes/reorder', send('PUT', items)),
  reorderInbounds: (nodeId: number, items: { id: number; sort_order: number }[]) =>
    request<unknown>(`/api/nodes/${nodeId}/inbounds/reorder`, send('PUT', items)),
  deleteInbound: (id: number) => request<unknown>(`/api/inbounds/${id}`, send('DELETE')),

  nodeStatus: (id: number) => request<NodeStatus>(`/api/nodes/${id}/status`),
  certRenew: (id: number, force = false) =>
    request<{ node: string; renewed: number; certs: CertRenewResult[] }>(
      `/api/nodes/${id}/cert-renew`,
      send('POST', { force }),
    ),
  setupSSH: (id: number, password: string) =>
    request<{ status: string }>(`/api/nodes/${id}/setup-ssh`, send('POST', { password })),
  install: (id: number) =>
    request<{ version: string }>(`/api/nodes/${id}/install`, send('POST', { version: 'latest' })),
  setupAssessment: (id: number, mode: string, domain: string) =>
    request<{ assessment?: SetupAssessment; error?: string } & Partial<SetupAssessment>>(
      `/api/nodes/${id}/setup-assessment?mode=${encodeURIComponent(mode)}&domain=${encodeURIComponent(domain)}`,
    ),
  autoSetup: (id: number, body: { domain: string; mode: string; protocols?: string[] }) =>
    request<{ inbounds?: { protocol: string; status: string }[] }>(
      `/api/nodes/${id}/auto-setup`,
      send('POST', body),
    ),
  certUpload: (id: number, body: { domain: string; cert: string; key: string }) =>
    request<{ cert_path?: string }>(`/api/nodes/${id}/cert-upload`, send('POST', body)),
  generate: (id: number) => request<unknown>(`/api/nodes/${id}/generate`, send('POST')),
  rawConfig: (id: number) => request<unknown>(`/api/nodes/${id}/raw-config`),
  push: (id: number) => request<SyncResult>(`/api/nodes/${id}/push`, send('POST')),
  pushAll: () => request<SyncResult[]>('/api/batch/push-all', send('POST')),

  statsMeta: () => request<StatsMeta>('/api/stats/meta'),
  usage: (q: UsageQuery) => request<UsageRow[]>(`/api/stats/usage?${usageParams(q)}`),
  userStats: () => request<UserStat[]>('/api/stats/users'),
  nodeStats: () => request<NodeStat[]>('/api/stats/nodes'),
}
