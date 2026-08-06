// 单位与展示格式的单一权威源：字节、时长、时刻、CSV。

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
export const GB = 1024 ** 3

export function fmtBytes(bytes?: number | null): string {
  if (!bytes) return '0 B'
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), UNITS.length - 1)
  return `${(bytes / 1024 ** i).toFixed(i > 0 ? 2 : 0)} ${UNITS[i]}`
}

export function fmtUptime(seconds?: number): string {
  if (!seconds) return '—'
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  return days > 0 ? `${days}d ${hours}h` : `${hours}h`
}

export function fmtClock(timestamp?: number): string {
  if (!timestamp) return ''
  return new Date(timestamp).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

export function bytesToGB(bytes: number): number {
  return bytes ? Math.round((bytes / GB) * 100) / 100 : 0
}

// CSV 导出：字段内的引号与分隔符按 RFC 4180 转义，BOM 让 Excel 正确识别 UTF-8
export function downloadCsv(filename: string, rows: (string | number)[][]) {
  const body = rows
    .map((row) => row.map((cell) => `"${String(cell).replace(/"/g, '""')}"`).join(','))
    .join('\n')
  const blob = new Blob([`﻿${body}`], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.click()
  URL.revokeObjectURL(url)
}
