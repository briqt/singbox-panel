import type { ThemeConfig } from 'antd'

// 统一调色板与主题：主色靛蓝 + 中性灰阶 + 语义色（借 antd 预设名保持协调），
// 集中在此定义，页面一律引用、不散落硬编码色值。

export const antdTheme: ThemeConfig = {
  token: {
    borderRadius: 8,
    fontFamily: "-apple-system, 'Segoe UI', 'PingFang SC', 'Noto Sans SC', sans-serif",
    colorBgLayout: '#ffffff', // 白底画布，侧栏与内容同色、只靠细线分隔
    colorPrimary: '#4f6ef2',
    colorBorderSecondary: '#eef0f3',
  },
  components: {
    Table: {
      headerBg: '#fafbfc',
      headerColor: '#5c6470',
      borderColor: '#f0f2f5',
      cellPaddingBlockSM: 7,
      cellPaddingInlineSM: 12,
    },
    Layout: { siderBg: '#ffffff' },
    Menu: {
      itemBorderRadius: 8,
      itemHeight: 42,
      itemSelectedBg: '#eef1fe',
      itemSelectedColor: '#4f6ef2',
      itemColor: '#5c6470',
    },
    Card: { headerFontSize: 15 },
    Segmented: {
      trackBg: '#f2f3f5',
      itemSelectedBg: '#eef1fe',
      itemSelectedColor: '#4f6ef2',
      itemHoverColor: '#4f6ef2',
      itemColor: '#5c6470',
      borderRadius: 8,
      borderRadiusSM: 6,
    },
  },
}

export const COLOR_PRIMARY = '#4f6ef2'
export const COLOR_ICON_MUTED = '#9aa1ac'

// 中性 token（与 antdTheme 内 token 同值，供 inline style 引用）
export const NEUTRAL = {
  border: '#eef0f3',
  borderTable: '#f0f2f5',
  bgSubtle: '#fcfcfd',
  textSecondary: '#5c6470',
} as const

// 语义 HEX（图表喂不进 antd 预设名）：上行主色、下行绿、告警橙、故障红
export const SEMANTIC_HEX = {
  up: '#4f6ef2',
  down: '#389e0d',
  warn: '#d46b08',
  fail: '#cf1322',
} as const

// 图表多序列配色（按用户/节点堆叠时循环取用）：同色系不同明度，避免撞色难分辨
export const SERIES_COLORS = [
  '#4f6ef2',
  '#389e0d',
  '#d46b08',
  '#722ed1',
  '#08979c',
  '#c41d7f',
  '#d4b106',
  '#1677ff',
  '#cf1322',
  '#5c6470',
] as const

export const seriesColor = (index: number) => SERIES_COLORS[index % SERIES_COLORS.length]

// 节点健康态与状态标签统一配色（值为 antd Tag/Badge 预设色名）
export const NODE_HEALTH_COLOR: Record<string, string> = {
  healthy: 'green',
  degraded: 'red',
  offline: 'red',
  stopped: 'red',
  missing: 'orange',
  partial: 'orange',
  certExpired: 'red',
  certExpiring: 'orange',
  unknown: 'default',
}

// 弹层宽度档位（单一权威）：杜绝散落魔法数
export const MODAL_WIDTH = { sm: 420, md: 560, lg: 720, xl: 900 } as const
