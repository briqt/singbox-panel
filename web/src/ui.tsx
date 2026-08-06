import type { CSSProperties, ReactNode } from 'react'
import { Card, Empty, Typography } from 'antd'
import { NEUTRAL } from './theme'

const { Title, Text } = Typography

// 统一页头：大标题 + 说明行，右侧可挂操作。集中定义，避免各页行距不一。
export function PageHeader({
  title,
  description,
  extra,
}: {
  title: ReactNode
  description?: ReactNode
  extra?: ReactNode
}) {
  return (
    <div
      style={{
        display: 'flex',
        justifyContent: 'space-between',
        alignItems: 'flex-start',
        gap: 16,
        flexWrap: 'wrap',
        marginBottom: 16,
      }}
    >
      <div>
        <Title level={4} style={{ margin: 0, lineHeight: 1.4 }}>
          {title}
        </Title>
        {description && (
          <Text type="secondary" style={{ display: 'block', marginTop: 6, lineHeight: 1.6 }}>
            {description}
          </Text>
        )}
      </div>
      {extra}
    </div>
  )
}

// 指标卡：左侧主色细条 + 大字号数值 + 脚注，概览与统计页共用一档视觉。
export function StatTile({
  label,
  value,
  foot,
  accent,
}: {
  label: ReactNode
  value: ReactNode
  foot?: ReactNode
  accent: string
}) {
  return (
    <Card size="small" style={{ position: 'relative', overflow: 'hidden' }}>
      <div style={{ position: 'absolute', left: 0, top: 0, bottom: 0, width: 3, background: accent }} />
      <Text type="secondary" style={{ fontSize: 12 }}>
        {label}
      </Text>
      <div style={{ fontSize: 24, fontWeight: 700, lineHeight: 1.25, fontVariantNumeric: 'tabular-nums' }}>
        {value}
      </div>
      {foot != null && (
        <Text type="secondary" style={{ fontSize: 12 }}>
          {foot}
        </Text>
      )}
    </Card>
  )
}

// 筛选栏：flex 换行 + 统一 gap/下距，控件由各页传入。
export function FilterBar({ children, style }: { children: ReactNode; style?: CSSProperties }) {
  return (
    <div
      style={{
        display: 'flex',
        flexWrap: 'wrap',
        alignItems: 'center',
        gap: 12,
        marginBottom: 12,
        ...style,
      }}
    >
      {children}
    </div>
  )
}

export function EmptyState({ description }: { description?: ReactNode }) {
  return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={description} />
}

// 等宽数字：流量、端口、时间这类需要竖向对齐的值一律走它
export function Mono({ children, color }: { children: ReactNode; color?: string }) {
  return (
    <span style={{ fontVariantNumeric: 'tabular-nums', color: color ?? NEUTRAL.textSecondary }}>
      {children}
    </span>
  )
}
