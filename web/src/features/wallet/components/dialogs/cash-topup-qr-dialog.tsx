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
import { QRCodeSVG } from 'qrcode.react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'

interface CashTopupQrDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  amount: string
  codeUrl: string
}

/**
 * Renders a WeChat Pay native code_url as a scannable QR code on the page.
 * Navigating to a weixin:// code_url would launch the desktop WeChat app,
 * so wallet recharge shows the QR here instead, matching the Alipay flow.
 */
export function CashTopupQrDialog({
  open,
  onOpenChange,
  amount,
  codeUrl,
}: CashTopupQrDialogProps) {
  const { t } = useTranslation()

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Scan with WeChat Pay')}
      description={t('Enter a CNY amount and scan with WeChat Pay')}
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
      </div>
    </Dialog>
  )
}
