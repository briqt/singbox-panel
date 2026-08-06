import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { App as AntApp, ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import enUS from 'antd/locale/en_US'
import 'dayjs/locale/zh-cn'
import './index.css'
import App from './App'
import { antdTheme } from './theme'
import { LangProvider, useI18n } from './i18n'

// antd 组件文案跟随界面语言切换（日期选择器、表格空态等）
function Localized() {
  const { lang } = useI18n()
  return (
    <ConfigProvider locale={lang === 'zh' ? zhCN : enUS} theme={antdTheme}>
      <AntApp>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </AntApp>
    </ConfigProvider>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <LangProvider>
      <Localized />
    </LangProvider>
  </StrictMode>,
)
