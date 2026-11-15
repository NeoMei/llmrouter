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
import { zodResolver } from '@hookform/resolvers/zod'
import { Loader2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { PasswordInput } from '@/components/password-input'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { LegalConsent } from '@/features/auth/components/legal-consent'
import { useAuthRedirect } from '@/features/auth/hooks/use-auth-redirect'
import {
  registrationDefinitivelyRejected,
  registrationThrottle,
} from '@/features/unihub/registration'
import { UniHubClient, UniHubError } from '@/features/unihub/sdk'
import type { ProductConfig } from '@/features/unihub/session'
import { useStatus } from '@/hooks/use-status'
import { cn } from '@/lib/utils'

// UniHub requires lowercase usernames that start with a letter. Keep the
// client-side rule aligned with internal/identityapi/server.go.
const centralUsername = /^[a-z][a-z0-9_-]{1,63}$/

const centralSignUpSchema = z
  .object({
    username: z
      .string()
      .min(1, 'Please enter your username')
      .regex(
        centralUsername,
        'Username must start with a letter and use only lowercase letters, digits, hyphens or underscores (2-64 characters)'
      ),
    email: z
      .string()
      .min(1, 'Please enter your email')
      .max(254, 'Please enter a valid email address')
      .regex(
        /^[^\s@<>"]+@[^\s@<>"@]+\.[^\s@<>"@]+$/,
        'Please enter a valid email address'
      ),
    password: z
      .string()
      .min(8, 'Password must be between 8 and 128 characters')
      .max(128, 'Password must be at most 128 characters'),
    confirmPassword: z.string().min(1, 'Please confirm your password'),
  })
  .refine((data) => data.password === data.confirmPassword, {
    message: "Passwords don't match.",
    path: ['confirmPassword'],
  })

type CentralSignUpValues = z.infer<typeof centralSignUpSchema>

export function CentralSignUpForm({
  config,
  className,
}: {
  config: ProductConfig
  className?: string
}) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const { redirectToLogin } = useAuthRedirect()
  const [isLoading, setIsLoading] = useState(false)
  const [agreedToLegal, setAgreedToLegal] = useState(false)
  const [retryAt, setRetryAt] = useState(0)
  const [retryNow, setRetryNow] = useState(Date.now)
  const [throttleNotice, setThrottleNotice] = useState('')
  // One intent per set of inputs. A UNKNOWN/throttled result must be retried
  // with the same idempotency key so the authority never creates a duplicate.
  const intent = useRef<{ body: CentralSignUpValues; key: string } | null>(null)
  const hasUserAgreement = Boolean(status?.user_agreement_enabled)
  const hasPrivacyPolicy = Boolean(status?.privacy_policy_enabled)
  const requiresLegalConsent = hasUserAgreement || hasPrivacyPolicy

  useEffect(() => {
    setAgreedToLegal(!requiresLegalConsent)
  }, [requiresLegalConsent])

  useEffect(() => {
    if (!retryAt) return
    const timer = window.setInterval(() => {
      const time = Date.now()
      setRetryNow(time)
      if (time >= retryAt) {
        setRetryAt(0)
        setThrottleNotice('')
      }
    }, 250)
    return () => window.clearInterval(timer)
  }, [retryAt])

  const form = useForm<CentralSignUpValues>({
    resolver: zodResolver(centralSignUpSchema),
    defaultValues: {
      username: '',
      email: '',
      password: '',
      confirmPassword: '',
    },
  })

  async function onSubmit(data: CentralSignUpValues) {
    if (requiresLegalConsent && !agreedToLegal) {
      toast.error(t('Please agree to the legal terms first'))
      return
    }
    if (retryAt > Date.now()) return

    const active =
      intent.current && intent.current.body.username === data.username
        ? intent.current
        : { body: data, key: crypto.randomUUID() }
    intent.current = active
    setIsLoading(true)
    try {
      const client = new UniHubClient({
        baseUrl: config.public_origin,
        clientId: config.client_id,
        redirectUri: config.redirect_uri,
      })
      const result = await client.register(
        {
          username: active.body.username,
          password: active.body.password,
          email: active.body.email,
        },
        active.key
      )
      if (result.status !== 'CREATED') {
        toast.error(
          t('Registration status unknown. Retry the original registration.')
        )
        return
      }
      intent.current = null
      toast.success(t('Account created! Please sign in'))
      redirectToLogin()
    } catch (error: unknown) {
      const throttle = registrationThrottle(error)
      if (throttle) {
        const time = Date.now()
        setRetryNow(time)
        setRetryAt(time + throttle.retryAfterMs)
        let notice = t(
          'Too many registration attempts. Please wait {{seconds}} seconds before trying again.',
          { seconds: Math.ceil(throttle.retryAfterMs / 1000) }
        )
        if (!throttle.notStarted) {
          notice =
            notice +
            ' ' +
            t(
              'Your original registration is still unresolved. Retry the same request.'
            )
        }
        setThrottleNotice(notice)
        return
      }
      if (error instanceof UniHubError && error.code === 'invalid_password') {
        toast.error(t('Password does not meet the security requirements'))
        intent.current = null
      } else if (
        error instanceof UniHubError &&
        error.code === 'interaction_required'
      ) {
        toast.error(t('Registration requires an additional verification step'))
        intent.current = null
      } else if (
        error instanceof UniHubError &&
        error.code === 'registration_conflict'
      ) {
        toast.error(t('This username or email is already in use'))
        intent.current = null
      } else if (
        error instanceof UniHubError &&
        error.code === 'idempotency_conflict'
      ) {
        toast.error(t('Registration conflict. Please refresh and try again.'))
        intent.current = null
      } else if (registrationDefinitivelyRejected(error)) {
        toast.error(t('Failed to create account'))
        intent.current = null
      } else {
        toast.error(t('Request failed'))
      }
    } finally {
      setIsLoading(false)
    }
  }

  const cooldown = Math.max(0, Math.ceil((retryAt - retryNow) / 1000))

  return (
    <Form {...form}>
      <form
        onSubmit={form.handleSubmit(onSubmit)}
        className={cn('grid gap-4', className)}
      >
        <FormField
          control={form.control}
          name='username'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Username')}</FormLabel>
              <FormControl>
                <Input placeholder={t('Enter your username')} {...field} />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='email'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Email')}</FormLabel>
              <FormControl>
                <Input
                  placeholder={t('name@example.com')}
                  type='email'
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='password'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Password')}</FormLabel>
              <FormControl>
                <PasswordInput
                  placeholder={t('Enter password (8-128 characters)')}
                  {...field}
                />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name='confirmPassword'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Confirm password')}</FormLabel>
              <FormControl>
                <PasswordInput placeholder={t('Confirm password')} {...field} />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />

        <LegalConsent
          status={status}
          checked={agreedToLegal}
          onCheckedChange={setAgreedToLegal}
          className='mt-1'
        />

        {cooldown > 0 ? (
          <p role='status' className='text-muted-foreground text-sm'>
            {throttleNotice}
          </p>
        ) : null}

        <Button
          type='submit'
          className='mt-2 w-full justify-center gap-2'
          disabled={
            isLoading ||
            cooldown > 0 ||
            (requiresLegalConsent && !agreedToLegal)
          }
        >
          {isLoading ? <Loader2 className='h-4 w-4 animate-spin' /> : null}
          {t('Create account')}
        </Button>
      </form>
    </Form>
  )
}
