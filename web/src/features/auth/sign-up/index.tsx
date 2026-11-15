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
import { Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  shouldShowLocalOperatorSignIn,
  shouldUseCentralSignUp,
} from '@/features/auth/sign-in/sign-in-mode'
import { UniHubClient } from '@/features/unihub/sdk'
import type { ProductConfig } from '@/features/unihub/session'
import { useStatus } from '@/hooks/use-status'

import { AuthLayout } from '../auth-layout'
import { TermsFooter } from '../components/terms-footer'
import { CentralSignUpForm } from './components/central-sign-up-form'
import { SignUpForm } from './components/sign-up-form'

export function SignUp() {
  const { t } = useTranslation()
  const { status } = useStatus()
  const [config, setConfig] = useState<ProductConfig | null | undefined>(
    undefined
  )
  const [configAttempt, setConfigAttempt] = useState(0)
  const [configFailed, setConfigFailed] = useState(false)
  const nativeRequested =
    new URLSearchParams(window.location.search).get('native') === '1'

  useEffect(() => {
    if (nativeRequested) {
      setConfig(null)
      setConfigFailed(false)
      return
    }
    setConfigFailed(false)
    setConfig(undefined)
    const controller = new AbortController()
    const loadConfig = async () => {
      try {
        const response = await fetch('/api/unihub/config', {
          credentials: 'same-origin',
          cache: 'no-store',
          signal: controller.signal,
        })
        if (!response.ok) throw new Error('configuration unavailable')
        const next = (await response.json()) as ProductConfig
        if (
          typeof next.enabled !== 'boolean' ||
          (next.native_access !== undefined &&
            typeof next.native_access !== 'boolean') ||
          (next.browser_sso_enabled !== undefined &&
            typeof next.browser_sso_enabled !== 'boolean')
        ) {
          throw new Error('invalid configuration')
        }
        if (next.enabled || next.browser_sso_enabled) {
          if (
            typeof next.public_origin !== 'string' ||
            typeof next.client_id !== 'string' ||
            typeof next.redirect_uri !== 'string' ||
            new URL(next.redirect_uri).origin !== window.location.origin
          ) {
            throw new Error('invalid configuration')
          }
          new UniHubClient({
            baseUrl: next.public_origin,
            clientId: next.client_id,
            redirectUri: next.redirect_uri,
          })
        }
        if (!controller.signal.aborted) {
          const central = shouldUseCentralSignUp(next, false)
          setConfig(central ? next : null)
        }
      } catch {
        if (!controller.signal.aborted) {
          setConfig(null)
          setConfigFailed(true)
        }
      }
    }
    void loadConfig()
    return () => controller.abort()
  }, [nativeRequested, configAttempt])

  return (
    <AuthLayout>
      <div className='w-full space-y-8'>
        <div className='space-y-2'>
          <h2 className='text-center text-2xl font-semibold tracking-tight sm:text-left'>
            {t('Create an account')}
          </h2>
          <p className='text-muted-foreground text-left text-sm sm:text-base'>
            {t('Already have an account?')}{' '}
            <Link
              to='/sign-in'
              className='hover:text-primary font-medium underline underline-offset-4'
            >
              {t('Sign in')}
            </Link>
            .
          </p>
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
        {!configFailed && config !== undefined && config ? (
          <CentralSignUpForm config={config} />
        ) : null}
        {!configFailed && config === null ? <SignUpForm /> : null}

        {shouldShowLocalOperatorSignIn(config) ? (
          <a href='/sign-up?native=1' className='text-muted-foreground text-sm'>
            {t('Local account sign up')}
          </a>
        ) : null}

        <TermsFooter
          variant='sign-up'
          status={status}
          className='text-center'
        />
      </div>
    </AuthLayout>
  )
}
