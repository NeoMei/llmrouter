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
import { CheckCircle2, Clock3, Loader2, RefreshCw, XCircle } from 'lucide-react'
import { QRCodeSVG } from 'qrcode.react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'

import type { WeChatNativeOrder } from '../../types'

interface WeChatNativePaymentDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  order: WeChatNativeOrder | null
  processing: boolean
  polling: boolean
  onRetry: () => void | Promise<boolean>
}

export function WeChatNativePaymentDialog({
  open,
  onOpenChange,
  order,
  processing,
  polling,
  onRetry,
}: WeChatNativePaymentDialogProps) {
  const { t } = useTranslation()

  if (!order) return null

  const pending = order.status === 'pending'
  const description = pending
    ? t('Pay exactly ¥{{amount}}', { amount: order.amount })
    : t('WeChat Pay order {{tradeNo}}', { tradeNo: order.trade_no })

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Scan with WeChat Pay')}
      description={description}
      contentClassName='max-sm:w-[calc(100vw-1.5rem)] sm:max-w-[420px]'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <Button variant='outline' onClick={() => onOpenChange(false)}>
          {t('Close')}
        </Button>
      }
    >
      <div className='flex min-h-64 flex-col items-center justify-center gap-4 py-3 text-center'>
        {pending && order.code_url ? (
          <div className='rounded-2xl border bg-white p-4 shadow-sm'>
            <QRCodeSVG
              value={order.code_url}
              size={208}
              level='M'
              marginSize={1}
              title={t('WeChat Pay QR code')}
            />
          </div>
        ) : null}

        {pending && !order.code_url ? (
          <div className='space-y-4'>
            <Clock3 className='mx-auto h-14 w-14 text-amber-500' />
            <p className='text-muted-foreground text-sm'>
              {t('QR code is not available yet. Retry with the same order.')}
            </p>
            <Button
              variant='outline'
              disabled={processing}
              onClick={() => void onRetry()}
            >
              {processing ? (
                <Loader2 className='mr-2 h-4 w-4 animate-spin' />
              ) : (
                <RefreshCw className='mr-2 h-4 w-4' />
              )}
              {t('Retry QR code')}
            </Button>
          </div>
        ) : null}

        {order.status === 'success' ? (
          <div className='space-y-3' role='status' aria-live='polite'>
            <CheckCircle2 className='mx-auto h-16 w-16 text-green-600' />
            <p className='text-lg font-semibold'>{t('Payment successful')}</p>
          </div>
        ) : null}

        {order.status === 'failed' ? (
          <div className='space-y-3' role='status' aria-live='polite'>
            <XCircle className='text-destructive mx-auto h-16 w-16' />
            <p className='text-lg font-semibold'>{t('Payment failed')}</p>
          </div>
        ) : null}

        {order.status === 'expired' ? (
          <div className='space-y-3' role='status' aria-live='polite'>
            <Clock3 className='text-muted-foreground mx-auto h-16 w-16' />
            <p className='text-lg font-semibold'>{t('Order expired')}</p>
          </div>
        ) : null}

        {pending ? (
          <div className='flex items-center gap-2 text-sm text-green-700 dark:text-green-400'>
            {polling ? <Loader2 className='h-4 w-4 animate-spin' /> : null}
            <span aria-live='polite'>{t('Waiting for payment')}</span>
          </div>
        ) : null}

        <p className='text-muted-foreground text-xs break-all'>
          {order.trade_no}
        </p>
      </div>
    </Dialog>
  )
}
