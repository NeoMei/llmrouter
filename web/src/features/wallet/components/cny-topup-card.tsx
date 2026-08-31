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
import { Loader2, QrCode } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { TitledCard } from '@/components/ui/titled-card'
import { cn } from '@/lib/utils'

interface CnyTopupCardProps {
  minAmount: string
  maxAmount: string
  processing: boolean
  onSubmit: (amount: string) => void | Promise<boolean>
}

const CNY_PRESET_AMOUNTS = ['10', '50', '100', '200', '500', '1000']
const EXACT_CNY_PATTERN = /^(0|[1-9]\d*)(?:\.\d{1,2})?$/

export function CnyTopupCard({
  minAmount,
  maxAmount,
  processing,
  onSubmit,
}: CnyTopupCardProps) {
  const { t } = useTranslation()
  const [amount, setAmount] = useState(minAmount)

  const limits = useMemo(
    () => ({ min: Number(minAmount), max: Number(maxAmount) }),
    [maxAmount, minAmount]
  )
  const numericAmount = Number(amount)
  const amountIsValid =
    EXACT_CNY_PATTERN.test(amount) &&
    Number.isFinite(numericAmount) &&
    numericAmount >= limits.min &&
    numericAmount <= limits.max
  const presets = CNY_PRESET_AMOUNTS.filter((preset) => {
    const value = Number(preset)
    return value >= limits.min && value <= limits.max
  })

  return (
    <TitledCard
      title={t('CNY top-up')}
      description={t('Enter a CNY amount and scan with WeChat Pay')}
      icon={<QrCode className='h-4 w-4' />}
      iconTone='success'
      disableHoverEffect
      contentClassName='space-y-4'
    >
      {presets.length > 0 ? (
        <div className='grid grid-cols-3 gap-2 sm:grid-cols-6'>
          {presets.map((preset) => (
            <Button
              key={preset}
              type='button'
              variant='outline'
              className={cn(
                amount === preset &&
                  'border-green-600 bg-green-500/10 text-green-700 dark:text-green-400'
              )}
              onClick={() => setAmount(preset)}
            >
              ¥{preset}
            </Button>
          ))}
        </div>
      ) : null}

      <div className='space-y-2'>
        <Label htmlFor='wechat-cny-amount'>{t('CNY amount')}</Label>
        <div className='relative'>
          <span className='text-muted-foreground pointer-events-none absolute top-1/2 left-3 -translate-y-1/2'>
            ¥
          </span>
          <Input
            id='wechat-cny-amount'
            inputMode='decimal'
            autoComplete='off'
            value={amount}
            onChange={(event) => setAmount(event.target.value.trim())}
            className='pl-7'
            aria-invalid={!amountIsValid}
          />
        </div>
        <p className='text-muted-foreground text-xs'>
          {t('Minimum ¥{{min}}, maximum ¥{{max}}', {
            min: minAmount,
            max: maxAmount,
          })}
        </p>
        {!amountIsValid ? (
          <p className='text-destructive text-xs' role='alert'>
            {t('Invalid CNY amount')}
          </p>
        ) : null}
      </div>

      <Button
        type='button'
        className='w-full bg-green-600 text-white hover:bg-green-700'
        disabled={!amountIsValid || processing}
        onClick={() => void onSubmit(amount)}
      >
        {processing ? (
          <Loader2 className='mr-2 h-4 w-4 animate-spin' />
        ) : (
          <QrCode className='mr-2 h-4 w-4' />
        )}
        {processing ? t('Creating QR code...') : t('Create WeChat Pay QR code')}
      </Button>
    </TitledCard>
  )
}
