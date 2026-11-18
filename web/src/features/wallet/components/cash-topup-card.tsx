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
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { TitledCard } from '@/components/ui/titled-card'
import { getFreshAuthHeaders, refreshAuthentication } from '@/lib/auth-session'

import { useCashCheckoutStatus } from '../hooks/use-cash-checkout-status'
import { cashPaymentPhase } from '../lib/cash-checkout-status'
import {
  CASH_TOPUP_TIERS_CNY,
  CashCheckoutError,
  cashCheckoutID,
  createCashCheckoutWithRetry,
  isNavigablePaymentURL,
  isWeChatQrPaymentURL,
  redirectToPayment,
  validateCashTopupAmount,
  type CashCheckout,
  type CashTopupProvider,
} from '../lib/cash-topup'
import { CashTopupQrDialog } from './dialogs/cash-topup-qr-dialog'

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
  payerIdentity: string
  account: string
  eligible: boolean
  assertIdentity: () => boolean
  onPaid: () => Promise<void>
}

export function CashTopupCard(props: CashTopupCardProps) {
  const { account, assertIdentity } = props
  const { t } = useTranslation()
  const [amount, setAmount] = useState('100')
  const [provider, setProvider] = useState<CashTopupProvider>('wechat')
  const [submitting, setSubmitting] = useState(false)
  const activeSubmit = useRef<AbortController | null>(null)
  const [error, setError] = useState('')
  const [checkout, setCheckout] = useState<CashCheckout | null>(null)
  const [checkoutID, setCheckoutID] = useState(() => {
    const returned =
      new URL(window.location.href).searchParams.get('cash_checkout') ?? ''
    if (cashCheckoutID.test(returned)) return returned
    try {
      const saved = sessionStorage.getItem(`cash-checkout:${account}`) ?? ''
      return cashCheckoutID.test(saved) ? saved : ''
    } catch {
      return ''
    }
  })
  const [qrOpen, setQrOpen] = useState(() => !!checkoutID)
  const status = useCashCheckoutStatus({ ...props, id: checkoutID, checkout })
  useEffect(() => {
    return () => activeSubmit.current?.abort()
  }, [])
  useEffect(() => {
    if (status.phase !== 'success' || !assertIdentity()) return
    try {
      const key = `cash-checkout:${account}`
      if (sessionStorage.getItem(key) === checkoutID) {
        sessionStorage.removeItem(key)
      }
    } catch {
      /* storage may be disabled */
    }
    const url = new URL(window.location.href)
    if (url.searchParams.get('cash_checkout') === checkoutID) {
      for (const key of [
        'cash_checkout',
        'out_trade_no',
        'trade_no',
        'trade_status',
        'total_amount',
        'seller_id',
        'app_id',
        'sign',
        'sign_type',
        'charset',
        'timestamp',
        'method',
        'version',
        'auth_app_id',
      ]) {
        url.searchParams.delete(key)
      }
      window.history.replaceState(window.history.state, '', url)
    }
  }, [status.phase, account, checkoutID, assertIdentity])

  if (!props.eligible || !account) return null

  const amountError = validateCashTopupAmount(amount)
  const canSubmit = amountError === null && !submitting

  const submit = async () => {
    if (activeSubmit.current || amountError !== null) return
    const request = new AbortController()
    activeSubmit.current = request
    const isCurrent = () => !request.signal.aborted && assertIdentity()
    setSubmitting(true)
    setError('')
    setQrOpen(false)
    try {
      const result = await createCashCheckoutWithRetry(
        {
          // Native fetch must be called with the window as its receiver.
          // Passing the bare global as a callback rebinds `this` to the deps
          // object when the library invokes `deps.fetch(...)`, which throws
          // "Illegal invocation" before any request is sent. Wrap it so the
          // call always goes through the global receiver.
          fetch: (...args: Parameters<typeof fetch>) =>
            globalThis.fetch(...args),
          getAuthHeaders: getFreshAuthHeaders,
          assertIdentity: isCurrent,
        },
        {
          payerIdentity: props.payerIdentity,
          account,
          amount,
          provider,
          productId: 'llmrouter',
        },
        () => refreshAuthentication()
      )
      if (!isCurrent()) return
      setCheckout(result)
      setCheckoutID(result.id)
      try {
        sessionStorage.setItem(`cash-checkout:${account}`, result.id)
      } catch {
        /* storage may be disabled */
      }
      const paymentURL = result.session?.url
      const phase = cashPaymentPhase({
        ...result,
        credited: result.credit_status === 'APPLIED',
      })
      if (
        !paymentURL ||
        phase !== 'waiting' ||
        isWeChatQrPaymentURL(paymentURL)
      ) {
        setQrOpen(true)
      } else {
        redirectToPayment(paymentURL, window.location)
      }
    } catch (cause) {
      if (!isCurrent()) return
      const code =
        cause instanceof CashCheckoutError ? cause.code : 'request_failed'
      setError(checkoutErrorMessage(code, t))
    } finally {
      if (activeSubmit.current === request) activeSubmit.current = null
      if (!request.signal.aborted) setSubmitting(false)
    }
  }

  const paymentURL = status.data?.session?.url ?? checkout?.session?.url ?? ''
  const showPaymentLink =
    status.phase === 'waiting' && isNavigablePaymentURL(paymentURL)
  const opensCashier = showPaymentLink && !isWeChatQrPaymentURL(paymentURL)
  const canOpenDialog =
    !!checkoutID && !opensCashier && status.phase !== 'success'

  return (
    <TitledCard
      title={t('Recharge with WeChat Pay or Alipay')}
      description={t(
        'Pay in CNY to fund your unified wallet for native API calls and subscriptions'
      )}
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
            {opensCashier ? (
              <a
                className='text-primary block text-center text-xs break-all underline underline-offset-4'
                href={paymentURL}
                target='_blank'
                rel='noopener noreferrer'
              >
                {t('Open payment page')}
              </a>
            ) : null}
            {canOpenDialog ? (
              <Button
                type='button'
                variant='link'
                className='text-primary block h-auto w-full p-0 text-xs underline underline-offset-4'
                disabled={status.checking}
                onClick={() => {
                  setQrOpen(true)
                  if (!showPaymentLink) void status.reconcile()
                }}
              >
                {showPaymentLink
                  ? t('Scan with WeChat Pay')
                  : t('Check payment status')}
              </Button>
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
      <CashTopupQrDialog
        open={qrOpen}
        onOpenChange={setQrOpen}
        amount={
          status.data
            ? (Number(status.data.amount) / 1000000).toFixed(2)
            : amount
        }
        phase={status.phase}
        checking={status.checking}
        checkError={status.checkError}
        onCheck={() => void status.reconcile()}
        codeUrl={isWeChatQrPaymentURL(paymentURL) ? paymentURL : ''}
      />
    </TitledCard>
  )
}
