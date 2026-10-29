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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { FinancialReceipt, type FinancialResult } from './financial-receipt'
import { UniHubError, type Quote, type Offer, type Recharge } from './sdk'
import { message, cnyMillionths, exactAmount, FinancialIntent } from './session'
import { Panel, Field, Submit, type PanelProps } from './ui'
import { upgradeSubscription } from './upgrades'

function isRecharge(value: FinancialResult): value is Recharge {
  return (
    !('offer' in value) &&
    !('kind' in value) &&
    'merchant' in value &&
    'currency' in value
  )
}

export function BillingPanel(props: PanelProps) {
  const { t } = useTranslation()
  const cache = useQueryClient()
  const [intent, setIntent] = useState<FinancialIntent | null>(
    () => props.session.intents.get(props.account.id) ?? null
  )
  const [result, setResult] = useState<FinancialResult>()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [quote, setQuote] = useState<Quote | null>(null)
  const finance = ['owner', 'billing_admin'].includes(props.account.role)
  const resources = useQuery({
    queryKey: ['resources', props.account.id],
    queryFn: () =>
      props.session.bound(() =>
        props.session.client.resources(props.account.id)
      ),
    retry: false,
  })
  const catalog = useQuery({
    queryKey: ['catalog'],
    queryFn: () => props.session.bound(() => props.session.client.catalog()),
    retry: false,
  })
  const orders = useQuery({
    queryKey: ['orders', props.account.id],
    queryFn: () =>
      props.session.bound(() => props.session.client.orders(props.account.id)),
    enabled: finance,
    retry: false,
  })
  const operations = useQuery({
    queryKey: ['operations', props.account.id],
    queryFn: () =>
      props.session.bound(() =>
        props.session.client.operations(props.account.id)
      ),
    enabled: finance,
    retry: false,
  })
  async function perform(original: FinancialIntent) {
    setBusy(true)
    setError('')
    setIntent(original)
    props.session.intents.set(original.account, original)
    try {
      original.assertAccount(props.account.id)
      const sdk = props.session.client
      let value: FinancialResult
      if (original.kind === 'recharge') {
        value = await props.session.bound(() =>
          sdk.createRecharge(
            original.account,
            'llmrouter',
            original.payload.amount,
            original.key
          )
        )
      } else if (original.kind === 'quote') {
        value = await props.session.bound(() =>
          sdk.acceptQuote(original.account, original.payload.id, original.key)
        )
      } else {
        value = await props.session.bound(() =>
          sdk.purchase(original.account, original.payload.id, original.key)
        )
      }
      setResult(value)
      if (original.kind === 'quote') setQuote(null)
      props.session.intents.delete(original.account)
      setIntent(null)
      await cache.invalidateQueries({
        queryKey: ['resources', props.account.id],
      })
      await cache.invalidateQueries({ queryKey: ['orders', props.account.id] })
      await cache.invalidateQueries({
        queryKey: ['operations', props.account.id],
      })
    } catch (e) {
      if (
        e instanceof UniHubError &&
        [400, 401, 403, 404, 422].includes(e.status)
      ) {
        props.session.intents.delete(original.account)
        setIntent(null)
      }
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  async function upgrade(offer: Offer) {
    setBusy(true)
    setError('')
    try {
      const subscription = upgradeSubscription(
        resources.data?.subscriptions ?? [],
        offer
      )
      if (!subscription) {
        throw new Error(t('Select an active subscription to quote an upgrade'))
      }
      setQuote(
        await props.session.bound(() =>
          props.session.client.quote(
            props.account.id,
            subscription.id,
            offer.id
          )
        )
      )
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  async function readback(form: HTMLFormElement) {
    setBusy(true)
    setError('')
    const data = new FormData(form)
    try {
      const id = String(data.get('id'))
      const kind = String(data.get('kind'))
      const sdk = props.session.client
      const value = await props.session.bound<FinancialResult>(() =>
        kind === 'recharge'
          ? sdk.getRecharge(props.account.id, id)
          : sdk.getOperation(props.account.id, id)
      )
      setResult(value)
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  async function queryResult(original: FinancialResult) {
    setBusy(true)
    setError('')
    try {
      const value = await props.session.bound(async () => {
        if ('offer' in original) {
          const orders = await props.session.client.orders(original.account_id)
          const order = orders.find((item) => item.id === original.id)
          if (!order) throw new Error('Original order unavailable')
          return order
        }
        if ('kind' in original) {
          return props.session.client.getOperation(
            original.account_id,
            original.id
          )
        }
        const recharge = await props.session.client.getRecharge(
          original.account_id,
          original.id
        )
        if (recharge.operation_id) {
          return props.session.client.getOperation(
            original.account_id,
            recharge.operation_id
          )
        }
        return recharge
      })
      setResult(value)
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  async function settleSandbox(recharge: Recharge) {
    if (
      !finance ||
      resources.isError ||
      resources.data?.payments.mode !== 'sandbox' ||
      recharge.account_id !== props.account.id ||
      recharge.product_id !== props.config.product_id
    )
      return
    setBusy(true)
    setError('')
    try {
      const operation = await props.session.bound(() =>
        props.session.client.sandboxSettleRecharge(
          recharge.account_id,
          recharge.id
        )
      )
      setResult(operation)
      await cache.invalidateQueries({
        queryKey: ['resources', props.account.id],
      })
      await cache.invalidateQueries({
        queryKey: ['operations', props.account.id],
      })
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  const offers =
    catalog.data?.filter(
      (offer) =>
        offer.product_id === 'llmrouter' &&
        (offer.account_kind === props.account.kind ||
          offer.account_kind === 'both') &&
        offer.status === 'published' &&
        (offer.kind === 'addon'
          ? resources.data?.billing_profile?.addons.enabled
          : resources.data?.billing_profile?.subscription.enabled)
    ) ?? []
  return (
    <Panel title='Resources and billing'>
      <p className='text-sm'>
        {t('Payer')}: {props.account.name} · {props.account.id} ·{' '}
        {props.account.role}
      </p>
      {resources.error ? <p role='alert'>{message(resources.error)}</p> : null}
      {resources.data ? (
        <>
          <p>
            {resources.data.payments.real_payments_configured
              ? t('Real payments configured')
              : t(
                  'Real payments unavailable. Creating an order does not pay or settle it.'
                )}
          </p>
          {resources.data.credits ? (
            <p>
              {t('Spendable credits')}:{' '}
              {exactAmount(resources.data.credits.spendable, 'CREDIT/6')} ·{' '}
              {t('Reserved')}:{' '}
              {exactAmount(resources.data.credits.reserved, 'CREDIT/6')} ·{' '}
              {t('Frozen')}:{' '}
              {exactAmount(resources.data.credits.frozen, 'CREDIT/6')}
            </p>
          ) : (
            <p>{t('Credit balance not visible')}</p>
          )}
          {resources.data.wallets?.map((wallet) => (
            <p key={wallet.product_id}>
              {wallet.product_id || t('Unified wallet')}:{' '}
              {exactAmount(wallet.amount, wallet.asset)}
            </p>
          ))}
          <h3 className='font-medium'>{t('Subscriptions')}</h3>
          {resources.data.subscriptions.length === 0 ? (
            <p>{t('No subscriptions')}</p>
          ) : (
            resources.data.subscriptions.map((sub) => (
              <div key={sub.id} className='rounded border p-3'>
                {sub.offer.name} · {sub.status} · {sub.id}
                <p>
                  {t('Paid through')}: {sub.paid_through}
                </p>
              </div>
            ))
          )}
        </>
      ) : null}
      {catalog.error ? <p role='alert'>{message(catalog.error)}</p> : null}
      <h3 className='font-medium'>{t('Plans and add-ons')}</h3>
      <div className='grid gap-3 md:grid-cols-2'>
        {offers.map((offer) => (
          <article className='space-y-2 rounded border p-3' key={offer.id}>
            <h4 className='font-medium'>{offer.name}</h4>
            <p>
              {offer.kind} · {offer.period} ·{' '}
              {exactAmount(offer.price, 'CNY/6')} ·{' '}
              {exactAmount(offer.monthly_credit, 'CREDIT/6')}
            </p>
            <p className='text-sm'>
              {offer.upgrade_rule} · {offer.refund_rule}
            </p>
            {offer.sandbox ? <p>{t('Sandbox offer')}</p> : null}
            {finance ? (
              <div className='flex gap-2'>
                <Button
                  disabled={busy || intent !== null}
                  onClick={() =>
                    void perform(
                      new FinancialIntent(props.account.id, 'purchase', {
                        id: offer.id,
                      })
                    )
                  }
                >
                  {t(
                    offer.kind === 'addon'
                      ? 'Purchase addon'
                      : 'Create purchase order'
                  )}
                </Button>
                {upgradeSubscription(
                  resources.data?.subscriptions ?? [],
                  offer
                ) ? (
                  <Button
                    variant='outline'
                    disabled={busy || intent !== null}
                    onClick={() => void upgrade(offer)}
                  >
                    {t('Quote upgrade')}
                  </Button>
                ) : null}
              </div>
            ) : null}
          </article>
        ))}
      </div>
      {quote ? (
        <div className='space-y-2 rounded border p-3'>
          <p>
            {t('Upgrade quote')}: {quote.id} ·{' '}
            {exactAmount(quote.amount, 'CNY/6')} · {t('Expires')}:{' '}
            {quote.expires_at}
          </p>
          <Button
            disabled={busy || intent !== null}
            onClick={() =>
              void perform(
                new FinancialIntent(props.account.id, 'quote', { id: quote.id })
              )
            }
          >
            {t('Accept quote and create order')}
          </Button>
        </div>
      ) : null}
      {finance ? (
        <>
          <form
            className='flex flex-wrap items-end gap-2'
            onSubmit={(e) => {
              e.preventDefault()
              const data = new FormData(e.currentTarget)
              try {
                void perform(
                  new FinancialIntent(props.account.id, 'recharge', {
                    amount: cnyMillionths(String(data.get('amount'))),
                  })
                )
              } catch (error) {
                setError(message(error))
              }
            }}
          >
            <Field name='amount' label='Recharge amount (CNY)' required />
            <Submit busy={busy || intent !== null} text='Create recharge' />
          </form>
          {intent ? (
            <div role='status' className='space-y-2 rounded border p-3'>
              <p>
                {t(
                  'Uncertain operation. Query its original object or retry with the same key.'
                )}
              </p>
              <p>
                {intent.account} · {intent.kind} · {intent.key}
              </p>
              <Button disabled={busy} onClick={() => void perform(intent)}>
                {t('Retry original operation')}
              </Button>
            </div>
          ) : null}
          <form
            className='flex flex-wrap items-end gap-2'
            onSubmit={(e) => {
              e.preventDefault()
              void readback(e.currentTarget)
            }}
          >
            <select name='kind' aria-label={t('Readback type')}>
              <option value='recharge'>{t('Recharge')}</option>
              <option value='operation'>{t('Operation')}</option>
            </select>
            <Field name='id' label='Original object ID' required />
            <Submit busy={busy} text='Query original object' />
          </form>
          <h3>{t('Orders')}</h3>
          {orders.error ? <p role='alert'>{message(orders.error)}</p> : null}
          {orders.data?.map((order) => (
            <div key={order.id} className='rounded border p-3'>
              {order.id} · {exactAmount(order.amount, 'CNY/6')} ·{' '}
              {order.payment_status} · {order.fulfillment_status}
            </div>
          ))}
          <Button
            variant='outline'
            disabled={busy}
            onClick={() =>
              void cache.invalidateQueries({
                queryKey: ['orders', props.account.id],
              })
            }
          >
            {t('Refresh order status')}
          </Button>
          <h3>{t('Operations')}</h3>
          {operations.error ? (
            <p role='alert'>{message(operations.error)}</p>
          ) : null}
          {operations.data?.map((operation) => (
            <p key={operation.id}>
              {operation.id} · {operation.kind} · {operation.status} ·{' '}
              {operation.error_code}
            </p>
          ))}
        </>
      ) : (
        <p>{t('Finance actions require owner or billing_admin role.')}</p>
      )}
      {result ? (
        <>
          <FinancialReceipt
            result={result}
            busy={busy}
            read={() => void queryResult(result)}
          />
          {finance &&
          !resources.isError &&
          resources.data?.payments.mode === 'sandbox' &&
          isRecharge(result) &&
          result.account_id === props.account.id &&
          result.product_id === props.config.product_id ? (
            <Button
              disabled={busy || intent !== null}
              onClick={() => void settleSandbox(result)}
            >
              {t('Settle sandbox recharge')}
            </Button>
          ) : null}
        </>
      ) : null}
      {error ? <p role='alert'>{error}</p> : null}
    </Panel>
  )
}
