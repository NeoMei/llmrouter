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
import { Link, useSearch } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { UniHubClient } from '@/features/unihub/sdk'
import type { ProductConfig } from '@/features/unihub/session'
import { silentProductLoginURL, startProductLogin } from '@/features/unihub/sso'
import { useStatus } from '@/hooks/use-status'
import { isSessionSelectionCleared } from '@/lib/auth-selection'

import { AuthLayout } from '../auth-layout'
import { AccountSwitchCard } from '../components/account-switch-card'
import { TermsFooter } from '../components/terms-footer'
import { UserAuthForm } from './components/user-auth-form'
import { shouldShowLocalOperatorSignIn } from './sign-in-mode'

export function SignIn() {
  const { t } = useTranslation()
  const { redirect } = useSearch({ from: '/(auth)/sign-in' })
  const { status } = useStatus()
  const [config, setConfig] = useState<ProductConfig | null | undefined>(
    undefined
  )
  const [configFailed, setConfigFailed] = useState(false)
  const [configAttempt, setConfigAttempt] = useState(0)

  useEffect(() => {
    if (new URLSearchParams(window.location.search).get('native') === '1') {
      setConfig(null)
      setConfigFailed(false)
      return
    }
    setConfigFailed(false)
    setConfig(undefined)
    const controller = new AbortController()
    void fetch('/api/unihub/config', {
      credentials: 'same-origin',
      cache: 'no-store',
      signal: controller.signal,
    })
      .then(async (response) => {
        if (!response.ok) throw new Error('configuration unavailable')
        const config = (await response.json()) as ProductConfig
        if (
          typeof config.enabled !== 'boolean' ||
          (config.browser_sso_enabled !== undefined &&
            typeof config.browser_sso_enabled !== 'boolean') ||
          (config.native_access !== undefined &&
            typeof config.native_access !== 'boolean')
        ) {
          throw new Error('invalid configuration')
        }
        if (config.enabled || config.browser_sso_enabled) {
          if (
            typeof config.public_origin !== 'string' ||
            typeof config.client_id !== 'string' ||
            typeof config.redirect_uri !== 'string' ||
            new URL(config.redirect_uri).origin !== window.location.origin
          ) {
            throw new Error('invalid configuration')
          }
          new UniHubClient({
            baseUrl: config.public_origin,
            clientId: config.client_id,
            redirectUri: config.redirect_uri,
          })
        }
        if (!controller.signal.aborted) {
          setConfig(
            (config.enabled || config.browser_sso_enabled) &&
              new URLSearchParams(window.location.search).get('native') !== '1'
              ? config
              : null
          )
        }
        if (
          controller.signal.aborted ||
          isSessionSelectionCleared() ||
          document.activeElement?.matches('input')
        ) {
          return
        }
        const target = silentProductLoginURL(
          config.enabled === true || config.browser_sso_enabled === true,
          window.location.search,
          redirect ?? '/dashboard',
          window.sessionStorage
        )
        if (target) window.location.replace(target)
      })
      .catch(() => {
        if (!controller.signal.aborted) setConfigFailed(true)
      })
    return () => controller.abort()
  }, [redirect, configAttempt])

  return (
    <AuthLayout>
      <div className='w-full space-y-8'>
        <div className='space-y-2'>
          <h2 className='text-center text-2xl font-semibold tracking-tight sm:text-left'>
            {t('Sign in')}
          </h2>
          {!status?.self_use_mode_enabled &&
            status?.register_enabled !== false && (
              <p className='text-muted-foreground text-left text-sm sm:text-base'>
                {t("Don't have an account?")}{' '}
                <Link
                  to='/sign-up'
                  className='hover:text-primary font-medium underline underline-offset-4'
                >
                  {t('Sign up')}
                </Link>
                .
              </p>
            )}
        </div>

        {configFailed ? (
          <div role='alert'>
            <p>{t('Request failed')}</p>
            <Button
              type='button'
              onClick={() => setConfigAttempt((n) => n + 1)}
            >
              {t('Try again')}
            </Button>
          </div>
        ) : null}
        {!configFailed && config === undefined ? <p>{t('Loading')}</p> : null}
        {!configFailed &&
        config !== undefined &&
        (!config || config.enabled) ? (
          <UserAuthForm redirectTo={redirect} productConfig={config} />
        ) : null}
        {!configFailed &&
        config &&
        !config.enabled &&
        config.browser_sso_enabled ? (
          <Button onClick={() => startProductLogin(redirect ?? '/dashboard')}>
            {t('Sign in with UniHub')}
          </Button>
        ) : null}
        {shouldShowLocalOperatorSignIn(config) ? (
          <a
            href={`/sign-in?native=1${redirect ? `&redirect=${encodeURIComponent(redirect)}` : ''}`}
          >
            {t('Local operator sign in')}
          </a>
        ) : null}

        <AccountSwitchCard />

        <TermsFooter
          variant='sign-in'
          status={status}
          className='text-center'
        />
      </div>
    </AuthLayout>
  )
}
