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
import { CheckCircle2, Loader2 } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'

import type { CashPaymentPhase } from '../../lib/cash-checkout-status'

interface CashTopupQrDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  amount: string
  codeUrl: string
  phase: CashPaymentPhase
  checking: boolean
  checkError: boolean
  onCheck: () => void
}

/**
 * Renders a WeChat Pay native code_url as a scannable QR code on the page.
 * Navigating to a weixin:// code_url would launch the desktop WeChat app,
 * so wallet recharge shows the QR here instead, matching the Alipay flow.
 */
export function CashTopupQrDialog(props: CashTopupQrDialogProps) {
  const { t } = useTranslation()
  const { open, onOpenChange, amount, codeUrl } = props
  const showQR = props.phase === 'waiting' && !!codeUrl
  let title = t('Payment status is being confirmed')
  let message = t('Waiting for payment')
  if (props.phase === 'success') {
    title = t('Payment successful')
    message = t('Recharge complete. Your balance has been updated.')
  } else if (props.phase === 'processing') {
    title = t('Payment successful')
    message = t('Payment received. Updating your balance.')
  } else if (props.phase === 'closed') {
    message = t('This payment is closed. Please create a new recharge.')
  } else if (props.phase === 'expired') {
    message = t(
      'This QR code has expired. Check the payment status before trying again.'
    )
  } else if (props.phase === 'review') {
    message = t('Payment received and requires review. Please contact support.')
  } else if (showQR) {
    title = t('Scan with WeChat Pay')
  }
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      description={
        showQR ? t('Enter a CNY amount and scan with WeChat Pay') : undefined
      }
      contentClassName='max-sm:w-[calc(100vw-1.5rem)] sm:max-w-[420px]'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <div className='flex w-full justify-end gap-2'>
          {props.phase !== 'success' ? (
            <Button
              variant='outline'
              disabled={props.checking}
              aria-busy={props.checking}
              onClick={props.onCheck}
            >
              {props.checking ? (
                <Loader2 className='animate-spin' aria-hidden='true' />
              ) : null}
              {t('Check payment status')}
            </Button>
          ) : null}
          <Button
            variant={props.phase === 'success' ? 'default' : 'outline'}
            onClick={() => onOpenChange(false)}
          >
            {props.phase === 'success' ? t('Back to wallet') : t('Close')}
          </Button>
        </div>
      }
    >
      <div className='flex min-h-64 flex-col items-center justify-center gap-4 py-3 text-center'>
        {showQR ? (
          <>
            <div className='rounded-2xl border bg-white p-4 shadow-sm'>
              <QRCodeSVG
                value={codeUrl}
                size={208}
                level='M'
                marginSize={1}
                title={t('WeChat Pay QR code')}
              />
            </div>
            <p className='text-sm font-medium'>
              {t('Pay exactly ¥{{amount}}', { amount })}
            </p>
          </>
        ) : null}
        {props.phase === 'success' ? (
          <CheckCircle2
            className='size-14 text-emerald-600'
            aria-hidden='true'
          />
        ) : null}
        {props.phase === 'processing' ? (
          <Loader2 className='size-12 animate-spin' aria-hidden='true' />
        ) : null}
        <p role='status' className='text-sm'>
          {message}
        </p>
        {props.checkError ? (
          <p role='alert' className='text-destructive text-sm'>
            {t('Unable to confirm payment. Please check again.')}
          </p>
        ) : null}
      </div>
    </Dialog>
  )
}
