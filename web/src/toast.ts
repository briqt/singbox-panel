import { useMemo } from 'react'
import { App } from 'antd'

// 反馈提示单一入口（fail-loud 收口）：err 把 unknown 归一为可读消息，不吞错、不假成功。
// message 来自 App.useApp() context，故做成 hook；useMemo 令返回对象稳定可入依赖数组。
export function useToast() {
  const { message } = App.useApp()
  return useMemo(
    () => ({
      ok: (text: string) => message.success(text),
      err: (error: unknown) => message.error(error instanceof Error ? error.message : String(error)),
      info: (text: string) => message.info(text),
    }),
    [message],
  )
}
