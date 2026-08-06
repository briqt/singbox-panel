import type { UsageRow } from './api'

// 用量行的日期与透视工具：后端只给聚合行，图表所需的连续日期轴与堆叠列在此展开。

export function shiftDay(day: string, delta: number): string {
  const date = new Date(`${day}T00:00:00Z`)
  date.setUTCDate(date.getUTCDate() + delta)
  return date.toISOString().slice(0, 10)
}

export function firstOfMonth(day: string): string {
  return `${day.slice(0, 7)}-01`
}

export function laterDay(a: string, b: string): string {
  return a > b ? a : b
}

// 区间内的每一天，含首尾；缺样本的日子也要在趋势图上占位，否则视觉上会被压缩掉
export function daysBetween(from: string, to: string): string[] {
  const days: string[] = []
  for (let day = from; day <= to; day = shiftDay(day, 1)) {
    days.push(day)
    if (days.length > 400) break // 留存上限之内不可能触发，纯粹兜底防死循环
  }
  return days
}

export function sumBytes(rows: UsageRow[]): number {
  return rows.reduce((total, row) => total + row.up + row.down, 0)
}

export interface StackedChart {
  data: Record<string, string | number>[]
  keys: string[]
}

// 把「按天 + 某维度」的行透视成 recharts 需要的宽表；列按区间总量降序，
// 图例顺序即贡献排序。
export function stackByDay(
  rows: UsageRow[],
  days: string[],
  label: (row: UsageRow) => string,
): StackedChart {
  const totals = new Map<string, number>()
  const cells = new Map<string, number>()
  for (const row of rows) {
    if (!row.day) continue
    const name = label(row)
    const bytes = row.up + row.down
    totals.set(name, (totals.get(name) ?? 0) + bytes)
    const cellKey = `${row.day}|${name}`
    cells.set(cellKey, (cells.get(cellKey) ?? 0) + bytes)
  }
  const keys = [...totals.entries()].sort((a, b) => b[1] - a[1]).map(([name]) => name)
  const data = days.map((day) => {
    const point: Record<string, string | number> = { day }
    for (const key of keys) point[key] = cells.get(`${day}|${key}`) ?? 0
    return point
  })
  return { data, keys }
}

// 上下行两条序列的日趋势（不拆分维度时用）
export function upDownByDay(rows: UsageRow[], days: string[]): Record<string, string | number>[] {
  const byDay = new Map<string, { up: number; down: number }>()
  for (const row of rows) {
    if (!row.day) continue
    const current = byDay.get(row.day) ?? { up: 0, down: 0 }
    byDay.set(row.day, { up: current.up + row.up, down: current.down + row.down })
  }
  return days.map((day) => ({ day, up: byDay.get(day)?.up ?? 0, down: byDay.get(day)?.down ?? 0 }))
}
