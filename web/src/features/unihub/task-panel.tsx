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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { useChatModels } from './chat-models'
import { consumeChat } from './gateway'
import type { ModelCredential, FundingMode } from './sdk'
import { exactAmount, message } from './session'
import { Panel, Field, Submit, Details, type PanelProps } from './ui'

export function TaskPanel(props: PanelProps) {
  const { t } = useTranslation()
  const cache = useQueryClient()
  const [grant, setGrant] = useState<ModelCredential | null>(null)
  const [call, setCall] = useState('')
  const [output, setOutput] = useState('')
  const [receipt, setReceipt] = useState<{
    call_id?: string
    status: string
    charged?: string
    asset: string
  }>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [model, setModel] = useState('')
  const [funding, setFunding] = useState<FundingMode | ''>('')
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  useEffect(() => {
    if (!grant) return
    const timer = setTimeout(
      () => setGrant(null),
      Math.max(0, Date.parse(grant.expires_at) - Date.now())
    )
    return () => clearTimeout(timer)
  }, [grant])
  const profile = useQuery({
    queryKey: ['profile'],
    queryFn: () =>
      props.session.bound(() =>
        props.session.client.billingProfile('llmrouter')
      ),
    retry: false,
  })
  const chatFunding =
    funding ||
    profile.data?.usage.default_funding_mode ||
    profile.data?.usage.funding_modes[0] ||
    ''
  const chat = useChatModels(props, chatFunding)
  const chatModels = chat.data?.models ?? []
  const selectedModel = chatModels.includes(model)
    ? model
    : (chatModels[0] ?? '')
  const selectedAsset =
    funding === 'prepaid_cash'
      ? 'CNY/6'
      : funding === 'credits'
        ? 'CREDIT/6'
        : ''
  async function readReceipt(current: ModelCredential, id: string) {
    const response = await fetch(
      `/platform/v1/calls/${encodeURIComponent(id)}`,
      {
        headers: {
          Authorization: `Bearer ${current.token}`,
          'X-UniHub-Task-ID': current.task_id,
        },
        signal: props.session.signal,
        credentials: 'omit',
      }
    )
    if (!response.ok) throw new Error(`Gateway receipt ${response.status}`)
    return response.json() as Promise<{
      call_id?: string
      status: string
      charged?: string
      asset: string
    }>
  }
  async function acceptReceipt(
    result: NonNullable<typeof receipt>,
    epoch: number
  ) {
    if (
      epoch !== props.session.epoch ||
      !mounted.current ||
      props.session.account !== props.account.id
    ) {
      return
    }
    setReceipt(result)
    if (result.status === 'SETTLED') {
      await Promise.all([
        cache.invalidateQueries({ queryKey: ['resources', props.account.id] }),
        cache.invalidateQueries({ queryKey: ['usage', props.account.id] }),
      ])
    }
  }
  async function refreshReceipt() {
    if (!grant) return
    const epoch = props.session.epoch
    setBusy(true)
    try {
      const result = await props.session.bound(() => readReceipt(grant, call))
      await acceptReceipt(result, epoch)
    } catch (error) {
      if (epoch === props.session.epoch && mounted.current) {
        setError(message(error))
      }
    } finally {
      if (epoch === props.session.epoch && mounted.current) setBusy(false)
    }
  }
  async function send(form: HTMLFormElement) {
    setBusy(true)
    setError('')
    setOutput('')
    setReceipt(undefined)
    const epoch = props.session.epoch
    const data = new FormData(form)
    try {
      if (!funding) throw new Error(t('Select funding mode explicitly'))
      if (
        !selectedModel ||
        !chat.data?.models.includes(selectedModel) ||
        selectedAsset !== chat.data.asset
      ) {
        throw new Error(t('No available models'))
      }
      const task = crypto.randomUUID(),
        id = crypto.randomUUID()
      const current = await props.session.bound(() =>
        props.session.client.issueModelCredential(
          props.account.id,
          [selectedModel],
          task,
          funding
        )
      )
      if (!mounted.current) return
      setGrant(current)
      setCall(id)
      const response = await fetch('/platform/v1/chat/completions', {
        method: 'POST',
        headers: {
          Authorization: `Bearer ${current.token}`,
          'Content-Type': 'application/json',
          'Idempotency-Key': id,
          'X-UniHub-Task-ID': task,
        },
        body: JSON.stringify({
          model: selectedModel,
          messages: [{ role: 'user', content: String(data.get('prompt')) }],
          max_tokens: Number(data.get('max_tokens')),
          n: 1,
          stream: data.get('stream') === 'on',
        }),
        signal: props.session.signal,
        credentials: 'omit',
        redirect: 'error',
      })
      if (!response.ok) {
        if (response.status === 401 && epoch === props.session.epoch) {
          props.session.clear()
        }
        throw new Error(`Gateway ${response.status}: ${await response.text()}`)
      }
      await consumeChat(
        response,
        data.get('stream') === 'on',
        (text) => setOutput((previous) => previous + text),
        () => epoch === props.session.epoch && mounted.current
      )
      if (epoch !== props.session.epoch || !mounted.current) return
      const result = await readReceipt(current, id)
      await acceptReceipt(result, epoch)
    } catch (e) {
      if (epoch === props.session.epoch && mounted.current) setError(message(e))
    } finally {
      if (mounted.current) setBusy(false)
    }
  }
  async function revoke() {
    if (!grant) return
    setBusy(true)
    try {
      await props.session.bound(() =>
        props.session.client.revokeModelCredential(props.account.id, grant.id)
      )
      setGrant(null)
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Panel title='Model task'>
      <p>
        {t(
          'Short-lived task credentials are not durable API keys. Text chat only; media, OCR and Responses are unavailable here.'
        )}
      </p>
      {chat.data &&
      (chatModels.length === 0 ||
        (funding && selectedAsset !== chat.data.asset)) ? (
        <p role='status'>{t('No available models')}</p>
      ) : null}
      {profile.error ? <p role='alert'>{message(profile.error)}</p> : null}
      {chat.error ? <p role='alert'>{message(chat.error)}</p> : null}
      <form
        className='grid gap-3'
        onSubmit={(e) => {
          e.preventDefault()
          void send(e.currentTarget)
        }}
      >
        <label>
          {t('Model')}
          <select
            className='ml-2'
            aria-label={t('Model')}
            value={selectedModel}
            onChange={(e) => setModel(e.target.value)}
            required
          >
            {chatModels.map((id) => (
              <option key={id}>{id}</option>
            ))}
          </select>
        </label>
        <label>
          {t('Funding mode')}
          <select
            className='ml-2'
            aria-label={t('Funding mode')}
            value={funding}
            onChange={(e) => setFunding(e.target.value as FundingMode)}
            required
          >
            <option value=''>{t('Choose funding mode')}</option>
            {profile.data?.usage.funding_modes.map((mode) => (
              <option key={mode}>{mode}</option>
            ))}
          </select>
        </label>
        <Field name='prompt' label='Message' required />
        <label>
          {t('Maximum output tokens')}
          <input
            className='ml-2 rounded border p-2'
            type='number'
            name='max_tokens'
            min='1'
            max='8192'
            defaultValue='256'
            required
          />
        </label>
        <label>
          <input type='checkbox' name='stream' /> {t('Stream response')}
        </label>
        <Submit
          text='Send new task'
          busy={
            busy ||
            !selectedModel ||
            !profile.data?.usage.enabled ||
            (Boolean(funding) && selectedAsset !== chat.data?.asset)
          }
        />
      </form>
      {grant ? (
        <div>
          <p>
            {t('Task credential')}: {grant.id} · {grant.task_id} ·{' '}
            {grant.account_id} · {grant.funding_mode} · {t('Expires')}:{' '}
            {grant.expires_at}
          </p>
          <Button
            disabled={busy}
            variant='outline'
            onClick={() => void revoke()}
          >
            {t('Revoke task credential')}
          </Button>
          <Button
            disabled={busy}
            variant='outline'
            onClick={() => void refreshReceipt()}
          >
            {t('Query task receipt')}
          </Button>
        </div>
      ) : null}
      {output ? (
        <pre
          aria-label={t('Model output')}
          className='bg-muted max-h-96 overflow-auto rounded p-3 whitespace-pre-wrap'
        >
          {output}
        </pre>
      ) : null}
      {receipt ? (
        <div>
          <p>
            {t('Task status')}: {receipt.status}
            {receipt.charged !== undefined
              ? ` · ${exactAmount(receipt.charged, receipt.asset)}`
              : ''}
          </p>
          <Details value={receipt} />
        </div>
      ) : null}
      {error ? <p role='alert'>{error}</p> : null}
    </Panel>
  )
}
