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
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { safeProductReturn, startProductLogin } from './sso'
import {
  registrationDefinitivelyRejected,
  registrationThrottle,
} from './registration'
import type { Registration } from './sdk'
import { message, type ProductSession } from './session'
import { Panel, Field, Submit } from './ui'

export function AuthPanel(props: { session: ProductSession }) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const [register, setRegister] = useState(false)
  const started = useRef(false)
  const query = new URLSearchParams(window.location.search)
  const failed = query.has('sso_error')
  const manual =
    query.has('signed_out') ||
    window.sessionStorage.getItem('unihub:signed-out') === '1'
  const destination = safeProductReturn(query.get('redirect'))
  useEffect(() => {
    if (!register && !failed && !manual && !started.current) {
      started.current = true
      startProductLogin(destination)
    }
  }, [register, failed, manual, destination])

  const sending = useRef(false)
  const [retryAt, setRetryAt] = useState(0)
  const [now, setNow] = useState(Date.now)
  const [throttled, setThrottled] = useState(false)
  useEffect(() => {
    if (!retryAt) return
    const timer = window.setInterval(() => {
      const time = Date.now()
      setNow(time)
      if (time >= retryAt) setRetryAt(0)
    }, 250)
    return () => window.clearInterval(timer)
  }, [retryAt])
  const [pending, setPending] = useState<{
    body: Registration
    key: string
    unresolved: boolean
  } | null>(null)
  async function submit(form: HTMLFormElement) {
    if (sending.current || (register && Date.now() < retryAt)) return
    sending.current = true
    setBusy(true)
    setThrottled(false)
    setNotice('')
    const data = new FormData(form)
    const body = {
      username: String(data.get('username')),
      password: String(data.get('password')),
      email: String(data.get('email') ?? ''),
    }
    const intent = pending ?? {
      body,
      key: crypto.randomUUID(),
      unresolved: false,
    }
    try {
      if (register) {
        setPending({ ...intent, unresolved: true })
        const result = await props.session.client.register(
          intent.body,
          intent.key,
        )
        if (result.status !== 'CREATED') {
          setNotice(
            t('Registration status unknown. Retry the original registration.'),
          )
          return
        }
        setPending(null)
        setNotice(t('Registration created. Sign in to continue.'))
        setRegister(false)
        form.reset()
        return
      }
      startProductLogin(destination)
    } catch (error) {
      if (!register && !props.session.user) props.session.clear()
      const throttle = register ? registrationThrottle(error) : null
      if (throttle) {
        setPending({
          ...intent,
          unresolved: intent.unresolved || !throttle.notStarted,
        })
        const time = Date.now()
        setNow(time)
        setRetryAt(time + throttle.retryAfterMs)
        setThrottled(true)
      } else {
        if (register && registrationDefinitivelyRejected(error)) {
          setPending(null)
        }
        setNotice(message(error))
      }
    } finally {
      sending.current = false
      setBusy(false)
    }
  }
  const cooldown = register && retryAt > now
  let statusText = notice
  if (throttled) {
    statusText = cooldown
      ? t(
          'Too many registration attempts. Please wait {{seconds}} seconds before trying again.',
          { seconds: Math.ceil((retryAt - now) / 1000) },
        )
      : t('Too many registration attempts. You can now retry manually.')
    if (pending?.unresolved) {
      statusText += ` ${t('Your original registration is still unresolved. Retry the same request.')}`
    }
  }
  let submitText = register ? 'Register' : 'Sign in'
  if (pending) submitText = 'Retry original registration'
  if (!register) {
    let status = 'Redirecting to UniHub sign in…'
    if (failed) status = 'UniHub sign in failed. Try again to continue.'
    else if (manual) status = 'Signed out'
    return (
      <Panel title='UniHub account'>
        <p>{t(status)}</p>
        <Button onClick={() => startProductLogin(destination)}>
          {t('Sign in with UniHub')}
        </Button>
        <Button variant='outline' onClick={() => setRegister(true)}>
          {t('Register UniHub account')}
        </Button>
      </Panel>
    )
  }
  return (
    <Panel title='UniHub account'>
      <p className='text-muted-foreground text-sm'>
        {t('Register UniHub account')}
      </p>
      <form
        className='grid max-w-md gap-3'
        onChange={() => {
          if (pending && !pending.unresolved) setPending(null)
        }}
        onSubmit={(e) => {
          e.preventDefault()
          void submit(e.currentTarget)
        }}
      >
        <fieldset
          disabled={busy || pending?.unresolved === true}
          className='grid gap-3'
        >
          <Field name='username' label='Username' required />
          <Field name='password' type='password' label='Password' required />
          {register ? <Field name='email' type='email' label='Email' /> : null}
        </fieldset>
        <Submit busy={busy || cooldown} text={submitText} />
      </form>
      <Button
        variant='outline'
        disabled={busy || pending?.unresolved === true}
        onClick={() => {
          setRegister(!register)
          setPending(null)
          setThrottled(false)
          setNotice('')
        }}
      >
        {t(register ? 'Back to sign in' : 'Register UniHub account')}
      </Button>
      {statusText ? <p role='status'>{statusText}</p> : null}
    </Panel>
  )
}
