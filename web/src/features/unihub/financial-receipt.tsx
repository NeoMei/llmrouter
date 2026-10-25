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
