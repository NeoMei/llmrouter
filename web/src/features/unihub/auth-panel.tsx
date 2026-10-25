import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { registrationDefinitivelyRejected } from './registration'
import type { User, Account, Registration } from './sdk'
import { message, type ProductSession } from './session'
import { Panel, Field, Submit } from './ui'

export function AuthPanel(props: {
  session: ProductSession
  onLogin: (user: User, accounts: Account[]) => void
}) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const [register, setRegister] = useState(false)
  const [pending, setPending] = useState<{
    body: Registration
    key: string
  } | null>(null)
  async function submit(form: HTMLFormElement) {
    setBusy(true)
    setNotice('')
    const data = new FormData(form)
    const body = {
      username: String(data.get('username')),
      password: String(data.get('password')),
      email: String(data.get('email') ?? ''),
    }
    try {
      if (register) {
        const intent = pending ?? { body, key: crypto.randomUUID() }
        setPending(intent)
        const result = await props.session.client.register(
          intent.body,
          intent.key
        )
        if (result.status !== 'CREATED') {
          setNotice(
            t('Registration status unknown. Retry the original registration.')
          )
          return
        }
        setPending(null)
        setNotice(t('Registration created. Sign in to continue.'))
        setRegister(false)
        form.reset()
        return
      }
      const identity = await props.session.bound(async () => {
        const tokens = await props.session.client.passwordLogin(body)
        props.session.expire(tokens.expires_in)
        const response = await fetch('/api/unihub/identity', {
          method: 'POST',
          headers: { Authorization: `Bearer ${tokens.access_token}` },
          credentials: 'omit',
          redirect: 'error',
          signal: props.session.signal,
        })
        if (!response.ok) throw new Error(t('Product identity proof rejected'))
        return (await response.json()) as {
          user: User
          accounts: Account[]
        }
      })
      props.onLogin(identity.user, identity.accounts)
      form.reset()
    } catch (error) {
      if (!register) props.session.clear()
      else if (registrationDefinitivelyRejected(error)) setPending(null)
      setNotice(message(error))
    } finally {
      setBusy(false)
    }
  }
  let submitText = register ? 'Register' : 'Sign in'
  if (pending) submitText = 'Retry original registration'
  return (
    <Panel title='UniHub account'>
      <p className='text-muted-foreground text-sm'>
        {t(
          'UniHub login is required again after refresh. Tokens stay in memory.'
        )}
      </p>
      <form
        className='grid max-w-md gap-3'
        onSubmit={(e) => {
          e.preventDefault()
          void submit(e.currentTarget)
        }}
      >
        <fieldset disabled={busy || pending !== null} className='grid gap-3'>
          <Field name='username' label='Username' required />
          <Field name='password' type='password' label='Password' required />
          {register ? <Field name='email' type='email' label='Email' /> : null}
        </fieldset>
        <Submit busy={busy} text={submitText} />
      </form>
      <Button
        variant='outline'
        disabled={busy || pending !== null}
        onClick={() => {
          setRegister(!register)
          setNotice('')
        }}
      >
        {t(register ? 'Back to sign in' : 'Register UniHub account')}
      </Button>
      {notice ? <p role='status'>{notice}</p> : null}
    </Panel>
  )
}
