import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { TeamPanel } from './account-panel'
import { AuthPanel } from './auth-panel'
import { BillingPanel } from './billing-panel'
import type { User, Account } from './sdk'
import {
  createProductSession,
  type ProductConfig,
  type ProductSession,
  message,
} from './session'
import { TaskPanel } from './task-panel'
import { UsagePanel } from './usage-panel'

function ProductShell(props: { config: ProductConfig }) {
  const { t } = useTranslation()
  const [session] = useState<ProductSession>(() =>
    createProductSession(props.config)
  )
  const [cache] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: { retry: false },
          mutations: { retry: false },
        },
      })
  )
  const [user, setUser] = useState<User | null>(null)
  const [accounts, setAccounts] = useState<Account[]>([])
  const [payer, setPayer] = useState('')
  const [error, setError] = useState('')
  useEffect(() => {
    session.onClear = () => {
      void cache.cancelQueries()
      cache.clear()
      setUser(null)
      setAccounts([])
      setPayer('')
    }
    return () => {
      session.onClear = () => {}
      session.clear()
      cache.clear()
    }
  }, [session, cache])
  async function refresh() {
    const result = await session.bound(() => session.client.accounts())
    const current = accounts.find((item) => item.id === payer)
    const fresh = result.find((item) => item.id === payer)
    if (current?.role !== fresh?.role) {
      session.select(fresh?.id ?? '')
      await cache.cancelQueries()
      cache.clear()
      if (!fresh) setPayer('')
    }
    setAccounts(result)
    await cache.invalidateQueries()
  }
  function select(id: string) {
    session.select(id)
    void cache.cancelQueries()
    cache.clear()
    setPayer(id)
  }
  const account = accounts.find((item) => item.id === payer)
  return (
    <QueryClientProvider client={cache}>
      {!user ? (
        <AuthPanel
          session={session}
          onLogin={(identity, list) => {
            setUser(identity)
            setAccounts(list)
          }}
        />
      ) : (
        <>
          <header className='flex flex-wrap items-center justify-between gap-3 rounded-xl border p-4'>
            <p>
              {user.name} · {user.id}
            </p>
            <Button variant='outline' onClick={() => session.clear()}>
              {t('Sign out')}
            </Button>
          </header>
          <section className='space-y-3 rounded-xl border p-4'>
            <label className='flex max-w-full min-w-0 flex-col items-start gap-3 sm:flex-row sm:items-center'>
              {t('Explicit payer account')}
              <select
                value={payer}
                onChange={(e) => select(e.target.value)}
                className='bg-background w-full max-w-full min-w-0 rounded border p-2 sm:flex-1'
              >
                <option value=''>{t('Select personal or team account')}</option>
                {accounts.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name} · {item.kind} · {item.role}
                  </option>
                ))}
              </select>
            </label>
            <Button
              variant='outline'
              onClick={() => void refresh().catch((e) => setError(message(e)))}
            >
              {t('Refresh accounts and roles')}
            </Button>
          </section>
          {account ? (
            <div key={`${account.id}:${account.role}`} className='space-y-5'>
              <TeamPanel
                session={session}
                account={account}
                config={props.config}
                refresh={refresh}
              />
              <BillingPanel
                session={session}
                account={account}
                config={props.config}
              />
              <TaskPanel
                session={session}
                account={account}
                config={props.config}
              />
              <UsagePanel
                session={session}
                account={account}
                config={props.config}
              />
            </div>
          ) : (
            <p>
              {t('Choose a payer before viewing resources or sending tasks.')}
            </p>
          )}
          {error ? <p role='alert'>{error}</p> : null}
        </>
      )}
    </QueryClientProvider>
  )
}
export function UniHub() {
  const { t } = useTranslation()
  const config = useQuery({
    queryKey: ['unihub-public-config'],
    queryFn: async () => {
      const res = await fetch('/api/unihub/config', { credentials: 'omit' })
      if (!res.ok) throw new Error('UniHub configuration unavailable')
      return res.json() as Promise<ProductConfig>
    },
    retry: false,
  })
  return (
    <main className='mx-auto max-w-6xl space-y-5 p-4 md:p-8'>
      <header className='flex items-center justify-between'>
        <h1 className='text-2xl font-semibold'>{t('LLMRouter · UniHub')}</h1>
        <Link to='/sign-in'>{t('Local operator sign in')}</Link>
      </header>
      {config.isPending ? <p>{t('Loading')}</p> : null}
      {config.error ? <p role='alert'>{message(config.error)}</p> : null}
      {config.data?.enabled ? <ProductShell config={config.data} /> : null}
      {config.data && !config.data.enabled ? (
        <p>{t('UniHub product integration is disabled by this deployment.')}</p>
      ) : null}
    </main>
  )
}
