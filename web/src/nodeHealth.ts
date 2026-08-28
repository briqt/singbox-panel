import type { NodeStatus } from './api'
import type { Translate } from './i18n'

// 节点健康态判定（单一权威）：从 /api/nodes/{id}/status 的原始字段派生一个可着色、可排序的态。
// 顺序即严重度：连不上 > 未装 > 没跑 > 证书过期 > 入站异常 > 监听未知 > 证书将过期 > 正常。
export type HealthState =
  | 'unknown'
  | 'offline'
  | 'missing'
  | 'stopped'
  | 'certExpired'
  | 'degraded'
  | 'partial'
  | 'certExpiring'
  | 'healthy'

// 证书告警阈值。acme.sh 自己在剩 60 天时续期、面板在剩 30 天时重签，
// 掉到 15 天说明这两道自动机制都已失灵，需要人介入。
export const CERT_WARN_DAYS = 15

export function healthState(status?: NodeStatus): HealthState {
  if (!status) return 'unknown'
  if (!status.reachable) return 'offline'
  if (!status.installed) return 'missing'
  if (!status.running) return 'stopped'
  // 证书过期比入站监听更靠前：hysteria2 的 UDP 监听在证书过期后照常开着，
  // 只看监听会把一个所有客户端都握手失败的节点报成健康。
  const certDays = status.cert_days_left
  if (certDays != null && certDays <= 0) return 'certExpired'
  const inbounds = status.inbounds ?? []
  if (inbounds.some((inbound) => inbound.listening === false)) return 'degraded'
  if (inbounds.some((inbound) => inbound.listening == null)) return 'partial'
  if (certDays != null && certDays < CERT_WARN_DAYS) return 'certExpiring'
  return 'healthy'
}

export function healthLabel(status: NodeStatus | undefined, t: Translate): string {
  const state = healthState(status)
  const inbounds = status?.inbounds ?? []
  const listening = inbounds.filter((inbound) => inbound.listening === true).length
  switch (state) {
    case 'offline':
      return t('healthOffline')
    case 'missing':
      return t('healthMissing')
    case 'stopped':
      return t('healthStopped')
    case 'certExpired':
      return t('healthCertExpired')
    case 'certExpiring':
      return t('healthCertExpiring', { days: status?.cert_days_left ?? 0 })
    case 'degraded':
      return t('healthDegraded', { listening, total: inbounds.length })
    case 'partial':
      return t('healthPartial')
    case 'healthy':
      return inbounds.length ? t('healthOk', { listening, total: inbounds.length }) : t('healthOkPlain')
    default:
      return t('healthPending')
  }
}
