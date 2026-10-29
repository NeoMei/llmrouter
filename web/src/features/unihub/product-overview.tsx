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
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { useChatModels } from './chat-models'
import { ManagedAccess } from './managed-access'
import { exactAmount, message } from './session'
import type { PanelProps } from './ui'

/** Displays only payer-scoped central resources, never local quota or keys. */
export function ProductOverview(props: PanelProps) {
  const { t } = useTranslation()
  const resources = useQuery({
    queryKey: ['resources', props.session.epoch, props.account.id],
    queryFn: () =>
      props.session.bound(() =>
        props.session.client.resources(props.account.id)
      ),
    retry: false,
  })
  const keys = useQuery({
    queryKey: [
      'managed-keys',
      props.session.epoch,
      props.account.id,
      props.config.product_id,
      props.config.instance_id,
    ],
    queryFn: () =>
      props.session.bound(() =>
        new ManagedAccess(
          props.session.client,
          props.account.id,
          props.config.product_id,
          props.config.instance_id ?? 'default'
        ).keys()
      ),
    retry: false,
  })
  const ownKeys = (keys.isError ? undefined : keys.data)?.filter(
    (key) =>
      key.actor_id === props.session.user?.id &&
      key.account_id === props.account.id &&
      key.origin_product_id === props.config.product_id &&
      key.instance_id === (props.config.instance_id ?? 'default')
  )
  const cash = useChatModels(
    props,
    ownKeys?.some((key) => key.funding_mode === 'prepaid_cash')
      ? 'prepaid_cash'
      : ''
  )
  const credits = useChatModels(
    props,
    ownKeys?.some((key) => key.funding_mode === 'credits') ? 'credits' : ''
  )
  const chatModel = ownKeys?.flatMap((key) => {
    const chat = key.funding_mode === 'prepaid_cash' ? cash : credits
    if (
      key.actor_id !== props.session.user?.id ||
      key.account_id !== props.account.id ||
      key.origin_product_id !== props.config.product_id ||
      key.instance_id !== (props.config.instance_id ?? 'default') ||
      key.asset !== chat.data?.asset ||
      key.revoked_at ||
      (key.expires_at &&
        (!Number.isFinite(Date.parse(key.expires_at)) ||
          Date.parse(key.expires_at) <= Date.now()))
    ) {
      return []
    }
    return key.models.filter(
      (model) =>
        chat.data?.models.includes(model) &&
        (key.operations == null ||
          key.operations[model]?.includes('chat.completions.create'))
    )
  })[0]
  return (
    <section className='space-y-4 rounded-xl border p-4'>
      <p>
        {t('Payer')}: {props.account.name}
      </p>
      {resources.isPending || keys.isPending ? <p>{t('Loading')}</p> : null}
      {resources.error ? <p role='alert'>{message(resources.error)}</p> : null}
      {keys.error ? <p role='alert'>{message(keys.error)}</p> : null}
      {cash.error ? <p role='alert'>{message(cash.error)}</p> : null}
      {credits.error ? <p role='alert'>{message(credits.error)}</p> : null}
      {!resources.isError &&
        resources.data?.wallets?.map((wallet) => (
          <p key={`${wallet.product_id}:${wallet.asset}`}>
            {wallet.product_id || t('Unified wallet')}:{' '}
            {exactAmount(wallet.amount, wallet.asset)}
          </p>
        ))}
      {!resources.isError && resources.data?.credits ? (
        <p>
          {t('Spendable credits')}:{' '}
          {exactAmount(resources.data.credits.spendable, 'CREDIT/6')}
        </p>
      ) : null}
      {!keys.isError && keys.data ? (
        <div>
          <p>
            {t('API Keys')}: {ownKeys?.length}
          </p>
          {ownKeys?.map((key) => (
            <p key={key.id}>{key.name}</p>
          ))}
        </div>
      ) : null}
      {chatModel ? (
        <>
          <p>
            {t('Model')}: {chatModel}
          </p>
          <pre className='overflow-auto whitespace-pre-wrap'>
            <code>{`curl ${window.location.origin}/v1/chat/completions -H 'Authorization: Bearer YOUR_API_KEY' -H 'Content-Type: application/json' -d '${JSON.stringify({ model: chatModel, messages: [{ role: 'user', content: 'Hello' }] }).replaceAll("'", "'\\''")}'`}</code>
          </pre>
        </>
      ) : !keys.isError &&
        keys.data &&
        (!cash.isEnabled || cash.data) &&
        (!credits.isEnabled || credits.data) ? (
        <p>{t('No available models')}</p>
      ) : null}
      <div className='flex gap-4'>
        <Link to='/keys'>{t('API Keys')}</Link>
        <Link to='/wallet'>{t('Wallet')}</Link>
      </div>
    </section>
  )
}
