import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

export function Panel(props: { title: string; children: ReactNode }) {
  const { t } = useTranslation()
  return (
    <section className='bg-card min-w-0 space-y-4 rounded-xl border p-5 break-words'>
      <h2 className='text-lg font-semibold'>{t(props.title)}</h2>
      {props.children}
    </section>
  )
}
export function Field(props: {
  label: string
  name: string
  type?: string
  value?: string
  onChange?: (value: string) => void
  required?: boolean
}) {
  const { t } = useTranslation()
  return (
    <label className='grid max-w-full min-w-0 gap-1 text-sm'>
      {t(props.label)}
      <Input
        name={props.name}
        type={props.type ?? 'text'}
        value={props.value}
        onChange={
          props.onChange ? (e) => props.onChange?.(e.target.value) : undefined
        }
        required={props.required}
        autoComplete={props.type === 'password' ? 'off' : undefined}
      />
    </label>
  )
}
export function Submit(props: { text: string; busy?: boolean }) {
  const { t } = useTranslation()
  return (
    <Button type='submit' disabled={props.busy}>
      {t(props.text)}
    </Button>
  )
}
export function Details(props: { value: unknown }) {
  const { t } = useTranslation()
  return (
    <details>
      <summary className='cursor-pointer text-sm'>
        {t('Technical details')}
      </summary>
      <pre className='bg-muted max-h-72 overflow-auto rounded-md p-3 text-xs break-all whitespace-pre-wrap'>
        {JSON.stringify(props.value, null, 2)}
      </pre>
    </details>
  )
}
export interface PanelProps {
  session: import('./session').ProductSession
  account: import('./sdk').Account
  config: import('./session').ProductConfig
}
