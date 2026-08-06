import type { NodeStatus } from './api'
import type { Translate } from './i18n'

// 节点健康态判定（单一权威）：从 /api/nodes/{id}/status 的原始字段派生一个可着色、可排序的态。
// 顺序即严重度：连不上 > 未装 > 没跑 > 入站异常 > 监听未知 > 正常。
export type HealthState = 'unknown' | 'offline' | 'missing' | 'stopped' | 'degraded' | 'partial' | 'healthy'

export function healthState(status?: NodeStatus): HealthState {
  if (!status) return 'unknown'
  if (!status.reachable) return 'offline'
  if (!status.installed) return 'missing'
  if (!status.running) return 'stopped'
  const inbounds = status.inbounds ?? []
  if (inbounds.some((inbound) => inbound.listening === false)) return 'degraded'
  if (inbounds.some((inbound) => inbound.listening == null)) return 'partial'
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
