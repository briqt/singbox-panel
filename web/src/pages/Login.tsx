import { useState } from 'react'
import { Button, Card, Divider, Form, Input, Typography } from 'antd'
import { api, saveSession } from '../api'
import type { Session } from '../api'
import { useI18n } from '../i18n'
import { useToast } from '../toast'
import { COLOR_PRIMARY } from '../theme'

const { Title, Text } = Typography

export default function Login({ onSignedIn }: { onSignedIn: (session: Session) => void }) {
  const { t } = useI18n()
  const toast = useToast()
  const [signingIn, setSigningIn] = useState(false)
  const [showRegister, setShowRegister] = useState(false)
  const [registering, setRegistering] = useState(false)

  const signIn = async (values: { username: string; password: string }) => {
    setSigningIn(true)
    try {
      const session = await api.login(values.username, values.password)
      saveSession(session)
      onSignedIn(session)
    } catch (error) {
      toast.err(error)
    } finally {
      setSigningIn(false)
    }
  }

  const register = async (values: { username: string; password: string }) => {
    setRegistering(true)
    try {
      await api.register(values.username, values.password)
      toast.ok(t('regSuccess'))
      setShowRegister(false)
    } catch (error) {
      toast.err(error)
    } finally {
      setRegistering(false)
    }
  }

  return (
    <div
      style={{
        height: '100vh',
        display: 'grid',
        placeItems: 'center',
        background: `radial-gradient(1200px 500px at 50% -10%, #eef1fe, transparent 60%)`,
      }}
    >
      <Card style={{ width: 360 }}>
        <div style={{ textAlign: 'center', marginBottom: 20 }}>
          <div
            style={{
              width: 44,
              height: 44,
              borderRadius: 12,
              margin: '0 auto 12px',
              background: COLOR_PRIMARY,
              color: '#fff',
              display: 'grid',
              placeItems: 'center',
              fontWeight: 700,
              fontSize: 20,
            }}
          >
            S
          </div>
          <Title level={4} style={{ margin: 0 }}>
            SingBox Panel
          </Title>
          <Text type="secondary">{t('loginHint')}</Text>
        </div>

        <Form layout="vertical" onFinish={signIn} requiredMark={false}>
          <Form.Item name="username" rules={[{ required: true }]} style={{ marginBottom: 12 }}>
            <Input size="large" placeholder={t('username')} autoComplete="username" />
          </Form.Item>
          <Form.Item name="password" rules={[{ required: true }]} style={{ marginBottom: 16 }}>
            <Input.Password size="large" placeholder={t('password')} autoComplete="current-password" />
          </Form.Item>
          <Button type="primary" size="large" htmlType="submit" block loading={signingIn}>
            {t('signIn')}
          </Button>
        </Form>

        <Divider style={{ margin: '16px 0 8px' }} plain>
          <Button type="link" size="small" onClick={() => setShowRegister(!showRegister)}>
            {t('register')}
          </Button>
        </Divider>

        {showRegister && (
          <Form layout="vertical" onFinish={register} requiredMark={false}>
            <Form.Item name="username" rules={[{ required: true }]} style={{ marginBottom: 12 }}>
              <Input placeholder={t('username')} />
            </Form.Item>
            <Form.Item
              name="password"
              rules={[{ required: true, min: 6 }]}
              extra={t('registerHint')}
              style={{ marginBottom: 12 }}
            >
              <Input.Password placeholder={t('password')} />
            </Form.Item>
            <Button htmlType="submit" block loading={registering}>
              {t('register')}
            </Button>
          </Form>
        )}
      </Card>
    </div>
  )
}
