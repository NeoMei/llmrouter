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
import { useTranslation } from 'react-i18next'

import { UniHubError } from './sdk'
import { message, exactAmount } from './session'
import { Details, Panel, type PanelProps } from './ui'

export function UsagePanel(props: PanelProps) {
  const { t } = useTranslation()
  const finance = ['owner', 'billing_admin'].includes(props.account.role)
  const usage = useQuery({
    queryKey: ['usage', props.account.id, props.account.role],
    queryFn: () =>
      props.session.bound(async () => {
        const sdk = props.session.client
        if (finance) {
          try {
            const [summary, calls] = await Promise.all([
              sdk.modelPoolUsage(props.account.id),
              sdk.modelPoolCalls(props.account.id),
            ])
            return { summary, calls, pool: true }
          } catch (e) {
            if (!(e instanceof UniHubError) || e.status !== 403) throw e
          }
        }
        const [summary, calls] = await Promise.all([
          sdk.usage(props.account.id),
          sdk.calls(props.account.id),
        ])
        return { summary, calls, pool: false }
      }),
    retry: false,
  })
  return (
    <Panel title='Usage and receipts'>
      {usage.isPending ? <p>{t('Loading')}</p> : null}
      {usage.error ? <p role='alert'>{message(usage.error)}</p> : null}
      {usage.data ? (
        <>
          <p>
            {t(
              usage.data.pool
                ? 'All-product model pool (finance visibility)'
                : 'Own-origin usage only. Other products are excluded.'
            )}
          </p>
          <p>
            {t('Calls')}: {usage.data.summary.calls_total} · {t('Pending')}:{' '}
            {usage.data.summary.pending_calls} · {usage.data.summary.as_of}
          </p>
          {usage.data.summary.financial_details &&
          usage.data.summary.charges_by_asset ? (
            Object.entries(usage.data.summary.charges_by_asset).map(
              ([asset, amount]) => (
                <p key={asset}>
                  {t('Charged')}: {exactAmount(amount, asset)}
                </p>
              )
            )
          ) : (
            <p>
              {usage.data.summary.financial_details &&
              usage.data.summary.charged !== undefined
                ? exactAmount(
                    usage.data.summary.charged,
                    usage.data.summary.asset
                  )
                : t('Financial amounts not visible')}
            </p>
          )}
          <Details
            value={{
              accuracy: usage.data.summary.accuracy,
              consistency: usage.data.summary.consistency,
              usage_complete: usage.data.summary.usage_complete,
            }}
          />
          <ul className='space-y-2'>
            {usage.data.calls.items.map((call) => (
              <li className='rounded border p-3' key={call.call_id}>
                {call.call_id} · {call.origin_product_id} · {call.model} ·{' '}
                {call.status}
                {call.financial_details && call.charged !== undefined ? (
                  <span> · {exactAmount(call.charged, call.asset)}</span>
                ) : null}
              </li>
            ))}
          </ul>
          {usage.data.calls.has_more ? (
            <p>{t('Only the first page is shown.')}</p>
          ) : null}
        </>
      ) : null}
    </Panel>
  )
}
