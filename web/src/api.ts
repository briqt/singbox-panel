// 类型与 fetch 封装：形状对齐后端真实返回（Go handler/*）。
// 错误约定：非 2xx 一律抛 Error，message = 后端 {"error": …}；401 触发登出回调。
// User / MeInfo / UserStat / NodeStat / UsageRow 的代理用量已由后端 ×2 折算，前端不可再乘。
// 配额上限不缩放；HostTraffic 的网卡、服务、端口等均为实测值，不折算。

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

export interface SocksOutbound {
  type: 'socks'
  server: string
  server_port: number
  username?: string
  password?: string
  network?: 'tcp'
}

// 节点间落地出口。只在面板与服务端配置中出现，不进订阅。
export interface ShadowsocksOutbound {
  type: 'shadowsocks'
  server: string
  server_port: number
  method: string
  password?: string
  network?: 'tcp'
}

export type Outbound = SocksOutbound | ShadowsocksOutbound

export interface Inbound {
  id: number
  node_id: number
  name: string
  tag: string
  protocol: string
  port: number
  enabled: boolean
  sort_order: number
  settings?: Record<string, unknown>
  outbound?: Outbound
  custom: boolean
  // 该线路在订阅里的名字，由后端按订阅同一算法给出；落地/停用线路为空。
  display_name?: string
}

export interface CreateInboundBody {
  name: string
  tag: string
  protocol: 'vless-reality'
  port: number
  outbound?: SocksOutbound
}

export interface UpdateInboundBody {
  name?: string
  tag?: string
  protocol?: string
  port?: number
  settings?: Record<string, unknown>
  outbound?: Outbound | null
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

export interface HostTrafficItem {
  kind: string
  name: string
  rx: number
  tx: number
  rx_packets: number
  tx_packets: number
  resets: number
}

export interface HostTrafficPeer {
  address: string
  bytes: number
  packets: number
}

export interface HostTrafficPoint {
  hour: number
  nic_rx: number
  nic_tx: number
  wan_rx: number
  wan_tx: number
  samples: number
  max_gap: number
  resets: number
  services: Record<string, number>
}

export interface HostTrafficSummary {
  node_id: number
  from: number
  to: number
  hours: number
  expected: number
  samples: number
  max_gap: number
  resets: number
  totals: HostTrafficItem[] | null
  series: HostTrafficPoint[] | null
  peers_from: number
  top_peers: { rx: HostTrafficPeer[] | null; tx: HostTrafficPeer[] | null }
  peer_other: { rx: { count: number; bytes: number }; tx: { count: number; bytes: number } }
}

export interface HostTrafficRetention {
  hourly_from: number
  peers_days: number
  node_buffer_hours: number
}

export interface TrafficAuditStatus {
  node: string
  installed: boolean
  healthy?: boolean
  reason?: string
  up_to_date?: boolean
  detail?: string
  status?: {
    interface?: string
    last_sample?: number
    last_sample_age?: number
    storage_bytes?: number
    first_sample?: number
  }
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
      inbound_ids?: number[]
      traffic_limit_bytes?: number
      expire_at?: string
      traffic_reset_day?: number
      note?: string
    },
  ) => request<{ sync?: SyncResult[] }>(`/api/users/${id}`, send('PUT', body)),
  deleteUser: (id: number) => request<unknown>(`/api/users/${id}`, send('DELETE')),
  resetTraffic: (id: number) => request<unknown>(`/api/users/${id}/reset-traffic`, send('POST')),
  resetSubToken: (id: number) => request<unknown>(`/api/users/${id}/reset-sub-token`, send('POST')),
  userAccess: (id: number) => request<Inbound[] | null>(`/api/users/${id}/access`),

  nodes: () => request<Node[] | null>('/api/nodes'),
  node: (id: number) => request<NodeDetail>(`/api/nodes/${id}`),
  createNode: (body: { name: string; host: string; domain: string; port: number; ssh_user: string }) =>
    request<Node>('/api/nodes', send('POST', { ...body, proxy_type: 'singbox' })),
  deleteNode: (id: number) => request<unknown>(`/api/nodes/${id}`, send('DELETE')),
  reorderNodes: (items: { id: number; sort_order: number }[]) =>
    request<unknown>('/api/nodes/reorder', send('PUT', items)),
  reorderInbounds: (nodeId: number, items: { id: number; sort_order: number }[]) =>
    request<unknown>(`/api/nodes/${nodeId}/inbounds/reorder`, send('PUT', items)),
  createInbound: (nodeId: number, body: CreateInboundBody) =>
    request<Inbound>(`/api/nodes/${nodeId}/inbounds`, send('POST', body)),
  updateInbound: (id: number, body: UpdateInboundBody) =>
    request<Inbound>(`/api/inbounds/${id}`, send('PUT', body)),
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
    request<{ version: string }>(`/api/nodes/${id}/install`, send('POST', { version: 'recommended' })),
  setupAssessment: (id: number, mode: string, domain: string) =>
    request<{ assessment?: SetupAssessment; error?: string } & Partial<SetupAssessment>>(
      `/api/nodes/${id}/setup-assessment?mode=${encodeURIComponent(mode)}&domain=${encodeURIComponent(domain)}`,
    ),
  autoSetup: (id: number, body: { domain: string; mode: string; protocols?: string[]; hysteria2_profile?: string; reality_sni?: string }) =>
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

  hostTraffic: (nodeId: number, hours: number) =>
    request<{ summary: HostTrafficSummary; retention: HostTrafficRetention }>(
      `/api/host-traffic?node_id=${nodeId}&hours=${hours}`,
    ),
  trafficAuditStatus: (nodeId: number) => request<TrafficAuditStatus>(`/api/nodes/${nodeId}/traffic-audit`),
  installTrafficAudit: (nodeId: number) =>
    request<unknown>(`/api/nodes/${nodeId}/traffic-audit`, send('POST', {})),
}

// 落地入站（protocol "shadowsocks"）只供其他节点做上游，不授权给用户。
export const isRelayInbound = (inbound: Pick<Inbound, 'protocol'>) => inbound.protocol === 'shadowsocks'

export function egressLabel(inbound: Pick<Inbound, 'protocol' | 'outbound' | 'settings'>, direct: string, relay: string): string {
  if (isRelayInbound(inbound)) {
    const sources = inbound.settings?.allowed_sources
    return `${relay} · ${Array.isArray(sources) ? sources.length : 0}`
  }
  const outbound = inbound.outbound
  if (!outbound) return direct
  const kind = outbound.type === 'shadowsocks' ? 'SS2022' : 'SOCKS5'
  return `${kind} · ${outbound.server}:${outbound.server_port}`
}
