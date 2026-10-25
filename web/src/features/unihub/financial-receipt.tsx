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

import type { Recharge, Order, Operation } from './sdk'
import { exactAmount } from './session'
import { Details } from './ui'

export type FinancialResult = Recharge | Order | Operation
export function FinancialReceipt(props: {
  result: FinancialResult
  busy: boolean
  read: () => void
}) {
  const { t } = useTranslation()
  const value = props.result
  const operation = 'kind' in value && 'status' in value
  let status = t('Created; payment and posting are not confirmed.')
  if (operation) {
    status =
      value.status === 'APPLIED'
        ? t('Posted to the ledger')
        : String(value.status)
  }
  if ('payment_status' in value) {
    status = `${value.payment_status} · ${value.fulfillment_status}`
  }
  return (
    <div role='status' className='space-y-2 rounded border p-3'>
      <h3 className='font-medium'>{t('Original financial object')}</h3>
      <p className='break-all'>{value.id}</p>
      {'amount' in value && typeof value.amount === 'string' ? (
        <p>{exactAmount(value.amount, 'CNY/6')}</p>
      ) : null}
      <p>{status}</p>
      <Button disabled={props.busy} variant='outline' onClick={props.read}>
        {t('Query this original object')}
      </Button>
      <Details value={value} />
    </div>
  )
}
