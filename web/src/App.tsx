import { lazy, Suspense, useCallback, useEffect, useState } from 'react'
import { Button, Layout, Menu, Space, Spin, Tooltip, Typography } from 'antd'
import {
  AreaChartOutlined,
  CloudServerOutlined,
  DashboardOutlined,
  LogoutOutlined,
  TeamOutlined,
  TranslationOutlined,
  UserOutlined,
} from '@ant-design/icons'
import { Link, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { COLOR_PRIMARY, NEUTRAL } from './theme'
import { clearSession, loadSession, setUnauthorizedHandler } from './api'
import type { Session } from './api'
import { useI18n } from './i18n'
// Login stays eager: it is the first paint for anyone without a session, and a
// suspense flash there would be the worst place to have one. The authenticated
// pages load on demand — Stats in particular drags in the whole charting
// library, which nobody needs until they open it.
import Login from './pages/Login'
const Overview = lazy(() => import('./pages/Overview'))
const Users = lazy(() => import('./pages/Users'))
const Nodes = lazy(() => import('./pages/Nodes'))
const Stats = lazy(() => import('./pages/Stats'))
const MyInfo = lazy(() => import('./pages/MyInfo'))

const { Sider, Content } = Layout
const { Text } = Typography

// 内容区可读上限：左对齐紧贴侧栏，流式增长到此封顶（1600 对齐 antd xxl 断点）
const CONTENT_MAX = 1600

export default function App() {
  const { pathname } = useLocation()
  const { t, lang, setLang } = useI18n()
  const [session, setSession] = useState<Session | null>(loadSession)

  const signOut = useCallback(() => {
    clearSession()
    setSession(null)
  }, [])

  // 令牌过期时 api 层回调到这里，界面直接退回登录页而不是停在空白列表
  useEffect(() => {
    setUnauthorizedHandler(() => setSession(null))
  }, [])

  if (!session) return <Login onSignedIn={setSession} />

  const isAdmin = session.role === 'admin'
  const selected = isAdmin
    ? pathname.startsWith('/users')
      ? '/users'
      : pathname.startsWith('/nodes')
        ? '/nodes'
        : pathname.startsWith('/stats')
          ? '/stats'
          : '/'
    : '/me'

  const items = isAdmin
    ? [
        { key: '/', icon: <DashboardOutlined />, label: <Link to="/">{t('overview')}</Link> },
        { key: '/users', icon: <TeamOutlined />, label: <Link to="/users">{t('users')}</Link> },
        { key: '/nodes', icon: <CloudServerOutlined />, label: <Link to="/nodes">{t('nodes')}</Link> },
        { key: '/stats', icon: <AreaChartOutlined />, label: <Link to="/stats">{t('stats')}</Link> },
      ]
    : [{ key: '/me', icon: <UserOutlined />, label: <Link to="/me">{t('myInfo')}</Link> }]

  return (
    <Layout style={{ height: '100vh', overflow: 'hidden' }}>
      <Sider theme="light" width={216} style={{ height: '100vh', borderInlineEnd: `1px solid ${NEUTRAL.border}` }}>
        <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
          <div style={{ height: 64, display: 'flex', alignItems: 'center', gap: 10, paddingInline: 20 }}>
            <div
              style={{
                width: 30,
                height: 30,
                borderRadius: 9,
                background: COLOR_PRIMARY,
                color: '#fff',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontWeight: 700,
                fontSize: 15,
                flexShrink: 0,
              }}
            >
              S
            </div>
            <div style={{ lineHeight: 1.2 }}>
              <div style={{ fontSize: 15, fontWeight: 600, whiteSpace: 'nowrap' }}>SingBox</div>
              <Text type="secondary" style={{ fontSize: 11 }}>
                {t('brandSub')}
              </Text>
            </div>
          </div>
          <Menu
            mode="inline"
            selectedKeys={[selected]}
            style={{ borderInlineEnd: 'none', paddingInline: 8, flex: 1 }}
            items={items}
          />
          <div style={{ padding: 12, borderTop: `1px solid ${NEUTRAL.border}` }}>
            <Space size={4} style={{ width: '100%', justifyContent: 'space-between' }}>
              <Text style={{ fontSize: 12, fontWeight: 600 }} ellipsis>
                {session.username}
              </Text>
              <Space size={0}>
                <Tooltip title={lang === 'zh' ? 'English' : '中文'}>
                  <Button
                    type="text"
                    size="small"
                    icon={<TranslationOutlined />}
                    onClick={() => setLang(lang === 'zh' ? 'en' : 'zh')}
                  />
                </Tooltip>
                <Tooltip title={t('signOut')}>
                  <Button type="text" size="small" icon={<LogoutOutlined />} onClick={signOut} />
                </Tooltip>
              </Space>
            </Space>
          </div>
        </div>
      </Sider>
      <Layout style={{ height: '100vh', overflow: 'hidden', background: '#fff' }}>
        <Content
          style={{
            flex: 1,
            minHeight: 0,
            overflowY: 'auto',
            scrollbarGutter: 'stable',
            padding: '24px 32px',
            maxWidth: CONTENT_MAX,
            width: '100%',
            boxSizing: 'border-box',
          }}
        >
          <Suspense
            fallback={
              <div style={{ display: 'flex', justifyContent: 'center', padding: '64px 0' }}>
                <Spin />
              </div>
            }
          >
            <Routes>
              {isAdmin ? (
                <>
                  <Route path="/" element={<Overview />} />
                  <Route path="/users" element={<Users />} />
                  <Route path="/nodes" element={<Nodes />} />
                  <Route path="/stats" element={<Stats />} />
                </>
              ) : (
                <Route path="/me" element={<MyInfo />} />
              )}
              <Route path="*" element={<Navigate to={isAdmin ? '/' : '/me'} replace />} />
            </Routes>
          </Suspense>
        </Content>
      </Layout>
    </Layout>
  )
}
