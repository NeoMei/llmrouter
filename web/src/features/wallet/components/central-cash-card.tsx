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
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { exactAmount } from '@/lib/funding'

import type { useCentralCashWallet } from '../hooks/use-central-cash-wallet'

export function CentralCashCard(props: {
  context: ReturnType<typeof useCentralCashWallet>
}) {
  const { t } = useTranslation()
  const data = props.context.data
  return (
    <section
      className='space-y-4 rounded-lg border p-4 sm:p-5'
      aria-label={t('Central cash wallets')}
    >
      <div className='grid gap-5 md:grid-cols-2'>
        <div className='min-w-0 space-y-2'>
          <h2 className='font-semibold'>{t('Native LLMRouter funding')}</h2>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Native API keys and subscriptions use unified cash. Recharge this wallet in UniHub, then return and refresh.'
            )}
          </p>
          {data ? (
            <a
              className='text-primary inline-block break-words underline underline-offset-4'
              href={data.rechargeURL}
              target='_blank'
              rel='noopener noreferrer'
            >
              {t('Recharge unified cash in UniHub')}
            </a>
          ) : null}
          <p className='text-muted-foreground text-xs'>
            {t(
              'Sandbox recharge requires your confirmation in UniHub. Opening this page does not create an order.'
            )}
          </p>
        </div>
        <div className='min-w-0 space-y-2 md:border-l md:pl-5'>
          <h2 className='font-semibold'>{t('Managed model wallet')}</h2>
          {data ? (
            <>
              <p className='font-mono text-2xl font-semibold break-all tabular-nums'>
                {exactAmount(data.product.amount, data.product.asset)}
              </p>
              <p className='text-muted-foreground text-xs break-all'>
                {t('Payer')}: {data.account.name} · {data.account.id}
              </p>
              <a
                className='text-primary inline-block break-words underline underline-offset-4'
                href={data.productURL}
                target='_blank'
                rel='noopener noreferrer'
              >
                {t('Manage model wallet in UniHub')}
              </a>
            </>
          ) : (
            <p
              className='text-muted-foreground'
              aria-label={t('Managed model wallet unavailable')}
            >
              —
            </p>
          )}
          <p className='text-muted-foreground text-sm'>
            {t(
              'For SuperWagie managed model calls. This balance does not fund native LLMRouter keys or subscriptions.'
            )}
          </p>
        </div>
      </div>
      {props.context.loading ? (
        <p role='status' className='text-muted-foreground text-sm'>
          {t('Verifying the central wallet payer…')}
        </p>
      ) : null}
      {!props.context.loading && !data ? (
        <div className='space-y-2'>
          <p role='alert' className='text-muted-foreground text-sm'>
            {t(
              'Central wallet details are unavailable. Refresh or sign in with UniHub to verify the payer.'
            )}
          </p>
          {props.context.canRetry ? (
            <Button
              variant='outline'
              onClick={() => void props.context.retry()}
            >
              {t('Retry central wallet')}
            </Button>
          ) : null}
        </div>
      ) : null}
    </section>
  )
}
