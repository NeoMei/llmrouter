import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'

import { formatQuantity, type NativePrice } from './contract'

type Bill = {
  id: string
  state: string
  platform: boolean
  reserved: string
  final?: string
  price?: NativePrice
  measurements?: {
    key: string
    unit: string
    quantity: { value: string; scale: number }
  }[]
}
type AdminBill = { ID: string; State: string; Platform: boolean }
export function NativeBillingPanel(props: { admin?: boolean }) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [selected, setSelected] = useState('')
  const [reason, setReason] = useState('')
  const [usage, setUsage] = useState('')
  const [actionID, setActionID] = useState('')
  const [action, setAction] = useState('confirm_measurement')
  const list = useQuery({
    queryKey: ['native-billing', Boolean(props.admin)],
    queryFn: async () =>
      (
        await api.get<{ data: Bill[] | AdminBill[] }>(
          props.admin ? '/api/native-billing/' : '/api/native-billing/self'
        )
      ).data.data,
  })
  const detail = useQuery({
    queryKey: ['native-billing-detail', selected],
    enabled: Boolean(props.admin && selected),
    queryFn: async () =>
      (await api.get(`/api/native-billing/${encodeURIComponent(selected)}`))
        .data as unknown,
  })
  const resolve = useMutation({
    mutationFn: async () =>
      api.post(`/api/native-billing/${encodeURIComponent(selected)}/resolve`, {
        action_id: actionID,
        action,
        reason,
        ...(action === 'confirm_measurement'
          ? { usage: JSON.parse(usage) as unknown }
          : {}),
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['native-billing'] })
      void client.invalidateQueries({ queryKey: ['native-billing-detail'] })
    },
  })
  return (
    <section className='space-y-3 overflow-auto rounded border p-3'>
      <h3 className='font-medium'>
        {t(
          props.admin
            ? 'Native billing reconciliation'
            : 'Native usage and receipts'
        )}
      </h3>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Pending measurements retain the reservation. A confirmed zero is a final zero charge.'
        )}
      </p>
      {list.isPending ? <p>{t('Loading')}</p> : null}
      {list.error ? <p role='alert'>{list.error.message}</p> : null}
      {list.data?.length === 0 ? <p>{t('No native billing records')}</p> : null}
      <ul className='space-y-2'>
        {list.data?.map((row) => {
          if ('ID' in row)
            {return (
              <li key={row.ID}>
                <Button
                  variant='outline'
                  onClick={() => {
                    setSelected(row.ID)
                    setActionID(crypto.randomUUID())
                  }}
                >
                  {row.ID} · {row.State}
                </Button>
              </li>
            )}
          return (
            <li className='rounded border p-3' key={row.id}>
              <p>
                {row.id} · {row.state}
              </p>
              <p>
                {t('Reserved')}: {row.reserved} {t('quota units')}
              </p>
              <p>
                {t('Final charge')}:{' '}
                {row.final === undefined ? t('Pending') : row.final}
              </p>
              {row.platform ? (
                <p>{t('Platform financial receipt is owned by UniHub.')}</p>
              ) : null}
              {row.price ? (
                <p>
                  {row.price.operation} · {row.price.policy} ·{' '}
                  {row.price.version}
                </p>
              ) : null}
              {row.measurements?.map((m) => (
                <p key={m.key}>
                  {m.key}: {formatQuantity(m.quantity)} {m.unit}
                </p>
              ))}
              {row.price?.terms.map((term) => (
                <p key={term.key}>
                  {term.key}: {term.price.numerator}/{term.price.denominator} /{' '}
                  {term.unit}
                </p>
              ))}
            </li>
          )
        })}
      </ul>
      {props.admin && selected ? (
        <div className='space-y-3'>
          <pre className='max-h-72 overflow-auto text-xs break-all whitespace-pre-wrap'>
            {JSON.stringify(detail.data, null, 2)}
          </pre>
          <label className='block'>
            {t('Resolution action')}
            <select
              className='w-full rounded border p-2'
              value={action}
              onChange={(e) => setAction(e.target.value)}
            >
              {[
                'confirm_measurement',
                'confirm_no_output',
                'operator_loss_release',
              ].map((a) => (
                <option key={a}>{a}</option>
              ))}
            </select>
          </label>
          <label className='block'>
            {t('Action ID')}
            <Input
              value={actionID}
              onChange={(e) => setActionID(e.target.value)}
            />
          </label>
          <label className='block'>
            {t('Reason')}
            <Input value={reason} onChange={(e) => setReason(e.target.value)} />
          </label>
          {action === 'confirm_measurement' ? (
            <label className='block'>
              {t('Confirmed usage JSON')}
              <Textarea
                value={usage}
                onChange={(e) => setUsage(e.target.value)}
              />
            </label>
          ) : null}
          <Button
            disabled={
              resolve.isPending ||
              !reason.trim() ||
              !actionID ||
              (action === 'confirm_measurement' && !usage)
            }
            onClick={() => resolve.mutate()}
          >
            {t('Resolve billing')}
          </Button>
          {resolve.error ? <p role='alert'>{resolve.error.message}</p> : null}
          {resolve.isSuccess ? <p>{t('Resolution recorded')}</p> : null}
        </div>
      ) : null}
      <p className='text-xs'>{t('Only the latest 100 records are shown.')}</p>
    </section>
  )
}
