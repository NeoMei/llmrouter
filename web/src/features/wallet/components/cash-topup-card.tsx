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
import { Loader2, Wallet } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { TitledCard } from '@/components/ui/titled-card'
import { getFreshAuthHeaders, refreshAuthentication } from '@/lib/auth-session'

import {
  CASH_TOPUP_TIERS_CNY,
  CashCheckoutError,
  createCashCheckoutWithRetry,
  isNavigablePaymentURL,
  redirectToPayment,
  validateCashTopupAmount,
  type CashCheckout,
  type CashTopupProvider,
} from '../lib/cash-topup'

function amountErrorMessage(
  code: ReturnType<typeof validateCashTopupAmount>,
  t: (key: string) => string
): string {
  if (code === 'belowMin') return t('Minimum recharge is ¥1')
  if (code === 'aboveMax') return t('Maximum recharge is ¥100000')
  return t('Enter a valid CNY amount (up to 2 decimals)')
}

function checkoutErrorMessage(
  code: string,
  t: (key: string) => string
): string {
  if (code === 'invalid') return t('Recharge amount is invalid')
  if (code === 'forbidden') {
    return t('You do not have permission to recharge this account')
  }
  if (code === 'payment_not_configured') {
    return t('Online payment is not configured yet. Please try again later.')
  }
  if (code === 'auth_retry') {
    return t('Session is refreshing. Please try again.')
  }
  if (code === 'access_retry') {
    return t('Session is refreshing. Please try again.')
  }
  if (code === 'missing_url') return t('Payment link unavailable')
  return t('Recharge request failed. Please try again.')
}

interface CashTopupCardProps {
  account: string
  eligible: boolean
  assertIdentity: () => boolean
}

export function CashTopupCard(props: CashTopupCardProps) {
  const { t } = useTranslation()
  const [amount, setAmount] = useState('100')
  const [provider, setProvider] = useState<CashTopupProvider>('wechat')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [checkout, setCheckout] = useState<CashCheckout | null>(null)

  if (!props.eligible || !props.account) return null

  const amountError = validateCashTopupAmount(amount)
  const canSubmit = amountError === null && !submitting

  const submit = async () => {
    if (validateCashTopupAmount(amount) !== null) return
    setSubmitting(true)
    setError('')
    setCheckout(null)
    try {
      const result = await createCashCheckoutWithRetry(
        {
          fetch: globalThis.fetch,
          getAuthHeaders: getFreshAuthHeaders,
          assertIdentity: props.assertIdentity,
        },
        {
          account: props.account,
          amount,
          provider,
          productId: 'llmrouter',
        },
        () => refreshAuthentication()
      )
      setCheckout(result)
      redirectToPayment(result.session.url, window.location)
    } catch (cause) {
      // Surface the real client-side throw during diagnosis: a bare TypeError
      // (e.g. crypto.randomUUID in a non-secure context) previously collapsed
      // into the generic request_failed message with no observable cause.
      console.error('[cash-topup] checkout failed', cause)
      const code =
        cause instanceof CashCheckoutError ? cause.code : 'request_failed'
      setError(checkoutErrorMessage(code, t))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <TitledCard
      title={t('Recharge with WeChat Pay or Alipay')}
      description={t('Pay in CNY to add cash to your LLMRouter product wallet')}
      icon={<Wallet className='h-4 w-4' />}
      iconTone='success'
      disableHoverEffect
      contentClassName='p-4 sm:p-5'
    >
      <div className='grid gap-6 md:grid-cols-2 md:gap-8'>
        <div className='min-w-0 space-y-3'>
          <Label htmlFor='cash-topup-amount' className='text-sm font-medium'>
            {t('Amount (CNY)')}
          </Label>
          <div className='relative'>
            <span className='text-muted-foreground pointer-events-none absolute top-1/2 left-3 -translate-y-1/2'>
              ¥
            </span>
            <Input
              id='cash-topup-amount'
              inputMode='decimal'
              autoComplete='off'
              value={amount}
              onChange={(event) => {
                setAmount(event.target.value.trim())
                setError('')
              }}
              className='h-11 pl-8 text-lg font-semibold tabular-nums'
              aria-invalid={amountError !== null}
              aria-describedby='cash-topup-amount-hint'
              disabled={submitting}
            />
          </div>
          <div
            className='grid grid-cols-3 gap-2 sm:grid-cols-5'
            role='group'
            aria-label={t('Amount (CNY)')}
          >
            {CASH_TOPUP_TIERS_CNY.map((tier) => (
              <Button
                key={tier}
                type='button'
                variant={amount === String(tier) ? 'default' : 'outline'}
                aria-pressed={amount === String(tier)}
                disabled={submitting}
                className='h-9 px-0 text-sm tabular-nums'
                onClick={() => {
                  setAmount(String(tier))
                  setError('')
                }}
              >
                ¥{tier}
              </Button>
            ))}
          </div>
          <p
            id='cash-topup-amount-hint'
            className='text-muted-foreground text-xs'
          >
            {t('Minimum ¥1, maximum ¥100000, up to 2 decimals')}
          </p>
        </div>

        <div className='min-w-0 space-y-3'>
          <Label className='text-sm font-medium'>{t('Payment method')}</Label>
          <div
            className='grid grid-cols-2 gap-3'
            role='group'
            aria-label={t('Payment method')}
          >
            {(['wechat', 'alipay'] as const).map((option) => (
              <Button
                key={option}
                type='button'
                variant={provider === option ? 'default' : 'outline'}
                aria-pressed={provider === option}
                disabled={submitting}
                className='h-11 text-sm font-medium'
                onClick={() => setProvider(option)}
              >
                {option === 'wechat' ? t('WeChat Pay') : t('Alipay')}
              </Button>
            ))}
          </div>
          <div className='space-y-2 pt-1'>
            <Button
              type='button'
              className='h-11 w-full'
              disabled={!canSubmit}
              onClick={() => void submit()}
            >
              {submitting ? (
                <Loader2 className='mr-2 h-4 w-4 animate-spin' />
              ) : null}
              {submitting
                ? t('Creating payment...')
                : t('Pay {{provider}}', {
                    provider:
                      provider === 'wechat' ? t('WeChat Pay') : t('Alipay'),
                  })}
            </Button>
            {checkout && isNavigablePaymentURL(checkout.session.url) ? (
              <a
                className='text-primary block text-center text-xs break-all underline underline-offset-4'
                href={checkout.session.url}
                target='_blank'
                rel='noopener noreferrer'
              >
                {t('Open payment page')}
              </a>
            ) : null}
          </div>
        </div>
      </div>

      {amountError !== null ? (
        <p role='alert' className='text-destructive mt-4 text-xs'>
          {amountErrorMessage(amountError, t)}
        </p>
      ) : null}
      {error ? (
        <p role='alert' className='text-destructive mt-4 text-xs'>
          {error}
        </p>
      ) : null}
    </TitledCard>
  )
}
