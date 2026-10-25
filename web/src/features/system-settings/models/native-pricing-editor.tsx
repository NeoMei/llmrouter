import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  catalogSchema,
  setReservationLimit,
  preview,
  policies,
  operationUnits,
  type NativePrice,
} from '@/features/native-metering/contract'

import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

type Props = { value: string; reservationLimits: string }
const emptyPrice: NativePrice = {
  version: 'metered_v2',
  operation: 'speech',
  policy: 'unicode_scalar_v1',
  terms: [
    {
      key: 'characters',
      unit: 'character',
      price: { numerator: '0', denominator: '1' },
    },
  ],
}
export function NativePricingEditor(props: Props) {
  const { t } = useTranslation()
  const update = useUpdateOption()
  const [model, setModel] = useState('')
  const [operation, setOperation] = useState('speech')
  const [policy, setPolicy] = useState('unicode_scalar_v1')
  const [unit, setUnit] = useState('character')
  const [price, setPrice] = useState('0')
  const [denominator, setDenominator] = useState('1')
  const [termKey, setTermKey] = useState('characters')
  const [namespace, setNamespace] = useState('')
  const [step, setStep] = useState('')
  const [minimum, setMinimum] = useState('')
  const [scale, setScale] = useState('0')
  const [sampleQuantity, setSampleQuantity] = useState('1500')
  const [raw, setRaw] = useState(props.value || '{}')
  const [limitModel, setLimitModel] = useState('')
  const [limitOperation, setLimitOperation] = useState<'speech' | 'video'>(
    'speech'
  )
  const [limitValue, setLimitValue] = useState('')
  const [rawLimits, setRawLimits] = useState(props.reservationLimits || '{}')
  useEffect(() => setRaw(props.value || '{}'), [props.value])
  useEffect(
    () => setRawLimits(props.reservationLimits || '{}'),
    [props.reservationLimits]
  )
  const parsed = useMemo(() => {
    try {
      return JSON.parse(raw) as Record<string, Record<string, NativePrice>>
    } catch {
      return null
    }
  }, [raw])
  const tariff: NativePrice = {
    ...emptyPrice,
    operation,
    policy,
    terms: [
      {
        key: termKey,
        unit,
        ...(namespace ? { provider_unit: namespace } : {}),
        price: { numerator: price, denominator },
        ...(step ? { step: { value: step, scale: Number(scale) } } : {}),
        ...(minimum
          ? { minimum: { value: minimum, scale: Number(scale) } }
          : {}),
      },
    ],
  }
  let sample = '—'
  try {
    sample = preview(tariff, {
      [termKey]: { value: sampleQuantity, scale: Number(scale) },
    })
  } catch {
    /* invalid drafts have no financial preview */
  }
  function save() {
    if (!model.trim()) {
      toast.error(t('Model is required'))
      return
    }
    const baseline = catalogSchema.safeParse(parsed)
    if (!baseline.success) {
      toast.error(t('Invalid native tariff'))
      return
    }
    const next = structuredClone(baseline.data)
    next[model] ||= {}
    const current = next[model][operation]
    const otherTerms =
      current?.terms.filter((term) => term.key !== termKey) || []
    next[model][operation] = {
      ...tariff,
      terms: [...otherTerms, ...tariff.terms],
    }
    const result = catalogSchema.safeParse(next)
    if (!result.success) {
      toast.error(t('Invalid native tariff'))
      return
    }
    const serialized = JSON.stringify(result.data, null, 2)
    setRaw(serialized)
    void update.mutateAsync({ key: 'ModelNativePricing', value: serialized })
  }
  function saveReservationLimit() {
    if (
      !limitModel.trim() ||
      !/^[1-9][0-9]{0,9}$/.test(limitValue) ||
      Number(limitValue) > 2147483647
    ) {
      toast.error(t('Invalid reservation limit'))
      return
    }
    let serialized: string
    try { serialized = setReservationLimit(rawLimits, limitModel, limitOperation, limitValue) } catch { toast.error(t('Invalid reservation limit')); return }
    setRawLimits(serialized)
    void update.mutateAsync({
      key: 'ModelNativeReservationLimits',
      value: serialized,
    })
  }
  return (
    <SettingsSection title={t('Native metering prices')}>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Explicit operation prices are opt-in. Platform prices remain owned by UniHub.'
        )}
      </p>{' '}
      <div className='space-y-3 rounded border p-3'>
        <h4 className='font-medium'>{t('Maximum native reservation')}</h4>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Maximum pre-consumption quantity for speech or video. Final billing always uses the actual provider receipt; this does not set a price.'
          )}
        </p>
        <div className='grid gap-3 md:grid-cols-3'>
          <Input
            aria-label={t('Reservation model')}
            value={limitModel}
            onChange={(e) => setLimitModel(e.target.value)}
            placeholder='model-name'
          />
          <select
            aria-label={t('Reservation operation')}
            className='rounded border p-2'
            value={limitOperation}
            onChange={(e) =>
              setLimitOperation(e.target.value as 'speech' | 'video')
            }
          >
            <option value='speech'>speech</option>
            <option value='video'>video</option>
          </select>
          <Input
            aria-label={t('Maximum quantity')}
            inputMode='numeric'
            value={limitValue}
            onChange={(e) => setLimitValue(e.target.value)}
            placeholder=''
          />
        </div>
        <Button
          type='button'
          onClick={saveReservationLimit}
          disabled={update.isPending}
        >
          {t('Save reservation limit')}
        </Button>
      </div>
      <div className='grid gap-3 md:grid-cols-2'>
        <label className='space-y-1'>
          <span className='text-sm'>{t('Model')}</span>
          <Input
            value={model}
            onChange={(e) => setModel(e.target.value)}
            placeholder='model-name'
          />
        </label>
        <label className='space-y-1'>
          <span className='text-sm'>{t('Operation')}</span>
          <select
            className='w-full rounded border p-2'
            value={operation}
            onChange={(e) => {
              const op = e.target.value
              setOperation(op)
              setPolicy(policies[op][0])
              const u = operationUnits[op][0]
              setUnit(u)
              setTermKey(
                (
                  {
                    token: 'input',
                    character: 'characters',
                    audio_second: 'audio',
                    image: 'images',
                    request: 'requests',
                    video_second: 'video',
                  } as Record<string, string>
                )[u] || 'provider'
              )
            }}
          >
            {Object.keys(policies).map((op) => (
              <option key={op}>{op}</option>
            ))}
          </select>
        </label>
        <label className='space-y-1'>
          <span className='text-sm'>{t('Policy')}</span>
          <select
            className='w-full rounded border p-2'
            value={policy}
            onChange={(e) => setPolicy(e.target.value)}
          >
            {policies[operation].map((p) => (
              <option key={p}>{p}</option>
            ))}
          </select>
        </label>
        <label className='space-y-1'>
          <span className='text-sm'>{t('Unit')}</span>
          <select
            className='w-full rounded border p-2'
            value={unit}
            onChange={(e) => {
              setUnit(e.target.value)
              if (e.target.value === 'provider_unit') setTermKey('provider')
              else if (e.target.value === 'video_second') setTermKey('video')
            }}
          >
            {operationUnits[operation].map((u) => (
              <option key={u}>{u}</option>
            ))}
          </select>
        </label>
        <label className='space-y-1'>
          <span className='text-sm'>{t('Price numerator')}</span>
          <Input
            inputMode='numeric'
            value={price}
            onChange={(e) => setPrice(e.target.value)}
          />
        </label>
        <label className='space-y-1'>
          <span className='text-sm'>{t('Price denominator')}</span>
          <Input
            inputMode='numeric'
            value={denominator}
            onChange={(e) => setDenominator(e.target.value)}
          />
        </label>
        {[
          ['Dimension or image tier', termKey, setTermKey],
          ['Provider namespace', namespace, setNamespace],
          ['Quantity scale', scale, setScale],
          ['Billing step', step, setStep],
          ['Minimum quantity', minimum, setMinimum],
          ['Preview quantity', sampleQuantity, setSampleQuantity],
        ].map(([label, value, change]) => (
          <label className='space-y-1' key={String(label)}>
            <span className='text-sm'>{t(String(label))}</span>
            <Input
              value={String(value)}
              onChange={(e) => (change as (v: string) => void)(e.target.value)}
            />
          </label>
        ))}
      </div>
      <p className='text-muted-foreground text-sm'>
        {t('Exact price preview: {{amount}} currency units', {
          amount: sample,
        })}
      </p>
      <Button type='button' onClick={save} disabled={update.isPending}>
        {t('Save native price')}
      </Button>
      <p>
        {t(
          'Advanced catalog supports multiple token dimensions and image tiers. Validate adapter coverage before saving.'
        )}
      </p>
      <Textarea
        aria-label={t('Native pricing JSON')}
        value={raw}
        onChange={(e) => setRaw(e.target.value)}
        className='font-mono text-xs'
        rows={8}
      />
      <Button
        type='button'
        disabled={update.isPending}
        onClick={() => {
          const result = catalogSchema.safeParse(parsed)
          if (!result.success) {
            toast.error(t('Invalid native tariff'))
            return
          }
          void update.mutateAsync({
            key: 'ModelNativePricing',
            value: JSON.stringify(result.data),
          })
        }}
      >
        {t('Save catalog JSON')}
      </Button>
    </SettingsSection>
  )
}
