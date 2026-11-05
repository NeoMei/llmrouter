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

import type { UsageLog } from '../data/schema'
import {
  measurementLabels,
  operationLabels,
  technicalUsage,
} from '../lib/technical-usage'
import type { LogOtherData } from '../types'

export function TechnicalUsage(props: { log: UsageLog; other: LogOtherData }) {
  const { t } = useTranslation()
  const hasLegacyTokens =
    props.log.prompt_tokens > 0 || props.log.completion_tokens > 0
  const usage = technicalUsage(props.other, hasLegacyTokens)
  const hasNativeTokens = usage.measurements.some((m) => m.unit === 'token')
  return (
    <div
      className='flex min-w-0 flex-col gap-1 text-xs break-words'
      aria-label={t('Native usage')}
    >
      <span>{t(usage.state)}</span>
      {props.other.operation && (
        <span>
          {t(operationLabels[props.other.operation] ?? 'Other operation')}
        </span>
      )}
      {usage.measurements.map((m) => (
        <span key={`${m.key}/${m.unit}/${m.provider_unit ?? ''}`}>
          {t(m.unit === 'image' ? 'Images' : measurementLabels[m.key])}
          {m.unit === 'image' && m.key !== 'images' && (
            <span className='whitespace-pre-wrap'>{` (${m.key})`}</span>
          )}
          {m.provider_unit && (
            <span className='whitespace-pre-wrap'>{` (${m.provider_unit})`}</span>
          )}
          : {m.formatted}
        </span>
      ))}
      {!hasNativeTokens && hasLegacyTokens && (
        <span>
          {t('Recorded input / output tokens')}: {props.log.prompt_tokens} /{' '}
          {props.log.completion_tokens}
        </span>
      )}
      {!hasNativeTokens && !hasLegacyTokens && (
        <span className='text-muted-foreground'>
          {t('Provider token usage was not recorded.')}
        </span>
      )}
    </div>
  )
}
