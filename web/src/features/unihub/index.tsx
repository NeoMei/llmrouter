/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { logout } from '@/features/auth/api'
import { clearAuthentication } from '@/lib/auth-session'
import { useAuthStore } from '@/stores/auth-store'

import { TeamPanel } from './account-panel'
import { AuthPanel } from './auth-panel'
import { BillingPanel } from './billing-panel'
import type { User, Account } from './sdk'
import {
  createProductSession,
  getActiveProductSession,
  type ProductConfig,
  type ProductSession,
  message,
} from './session'
import { TaskPanel } from './task-panel'
import { UsagePanel } from './usage-panel'

function ProductShell(props: { config: ProductConfig }) {
  const { t } = useTranslation()
  const [session] = useState<ProductSession>(
    () => getActiveProductSession() ?? createProductSession(props.config)
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
  const navigate = useNavigate()
  const [user, setUser] = useState<User | null>(session.user)
  const [accounts, setAccounts] = useState<Account[]>(session.accounts)
  const [payer, setPayer] = useState(session.account)
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
    session.accounts = result
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
            session.user = identity
            session.accounts = list
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
            <Button
              variant='outline'
              onClick={() => {
                void logout()
                  .then(() => {
                    session.clear()
                    clearAuthentication()
                    void navigate({ to: '/unihub', replace: true })
                  })
                  .catch((e) => setError(message(e)))
              }}
            >
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
  const projected = useAuthStore(
    (state) => state.auth.user?.identity_source === 'unihub'
  )
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
        {config.data?.native_access && projected ? (
          <nav aria-label={t('UniHub account')}>
            <Link to='/dashboard'>{t('Console')}</Link>
          </nav>
        ) : config.data?.native_access ? (
          <Link to='/sign-in'>{t('Local operator sign in')}</Link>
        ) : null}
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
