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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

const ratioPart = /^[1-9][0-9]{0,8}$/
const pricingRevision = /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/
const supplierCatalogSchema = z.record(z.string(), z.unknown())
const policySchema = z.object({
  version: z.string().regex(pricingRevision),
  multiplier: z
    .object({
      numerator: z.string().regex(ratioPart),
      denominator: z.string().regex(ratioPart),
    })
    .default({ numerator: '3', denominator: '2' }),
  suppliers: supplierCatalogSchema.default({}),
})

function readSavedPolicy(value: string) {
  try {
    return policySchema.parse(JSON.parse(value))
  } catch {
    return null
  }
}

export function ProductCreditPricingEditor(props: { value: string }) {
  const { t } = useTranslation()
  const update = useUpdateOption()
  const [saved] = useState(() => readSavedPolicy(props.value))
  const [revision, setRevision] = useState(saved?.version ?? '')
  const [numerator, setNumerator] = useState(saved?.multiplier.numerator ?? '3')
  const [denominator, setDenominator] = useState(
    saved?.multiplier.denominator ?? '2'
  )
  const [supplierJSON, setSupplierJSON] = useState(() =>
    JSON.stringify(saved?.suppliers ?? {}, null, 2)
  )
  const validPolicy =
    !!saved &&
    pricingRevision.test(revision) &&
    ratioPart.test(numerator) &&
    ratioPart.test(denominator)

  function save() {
    if (!validPolicy) return
    try {
      const suppliers = supplierCatalogSchema.parse(JSON.parse(supplierJSON))
      update.mutate({
        key: 'ProductCreditPricing',
        value: JSON.stringify({
          version: revision,
          multiplier: { numerator, denominator },
          suppliers,
        }),
      })
    } catch {
      toast.error(t('Invalid supplier tariff JSON'))
    }
  }

  return (
    <SettingsSection title={t('Product credit pricing')}>
      <p className='text-muted-foreground text-sm'>
        {t(
          'The multiplier applies to the highest comparable supplier price for each billing dimension. The published native price applies when no supplier price is configured.'
        )}
      </p>
      <div className='grid gap-3 sm:grid-cols-3'>
        <label className='space-y-1'>
          <span className='text-sm'>{t('Pricing revision')}</span>
          <Input
            value={revision}
            maxLength={128}
            onChange={(event) => setRevision(event.target.value)}
          />
        </label>
        <label className='space-y-1'>
          <span className='text-sm'>{t('Multiplier numerator')}</span>
          <Input
            value={numerator}
            inputMode='numeric'
            maxLength={9}
            onChange={(event) => setNumerator(event.target.value)}
          />
        </label>
        <label className='space-y-1'>
          <span className='text-sm'>{t('Multiplier denominator')}</span>
          <Input
            value={denominator}
            inputMode='numeric'
            maxLength={9}
            onChange={(event) => setDenominator(event.target.value)}
          />
        </label>
      </div>
      <label className='space-y-1'>
        <span className='text-sm'>{t('Supplier pay-as-you-go tariffs')}</span>
        <Textarea
          rows={8}
          className='font-mono text-xs'
          value={supplierJSON}
          onChange={(event) => setSupplierJSON(event.target.value)}
        />
      </label>
      <Button
        type='button'
        disabled={!validPolicy || update.isPending}
        onClick={save}
      >
        {t('Save product credit pricing')}
      </Button>
    </SettingsSection>
  )
}
