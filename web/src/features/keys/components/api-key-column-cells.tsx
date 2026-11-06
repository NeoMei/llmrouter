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
import type { CellContext } from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'

import { BadgeCell, TruncatedCell } from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { StatusBadge } from '@/components/status-badge'
import { Progress } from '@/components/ui/progress'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { formatQuota } from '@/lib/format'
import { cn } from '@/lib/utils'

import { API_KEY_STATUSES } from '../constants'
import type { ApiKey } from '../types'
import { ApiKeyTimestampCell } from './api-key-timestamp-cell'
import { UnlimitedQuotaBadge } from './api-keys-cells'

export type ApiKeyTimeColumnMeta = {
  now: number
  locale: string
  justNowLabel: string
}

export type ApiKeyGroupColumnMeta = {
  groupRatios: Record<string, number>
}

function getQuotaProgressColor(percentage: number): string {
  if (percentage <= 10) return '[&_[data-slot=progress-indicator]]:bg-rose-500'
  if (percentage <= 30) return '[&_[data-slot=progress-indicator]]:bg-amber-500'
  return '[&_[data-slot=progress-indicator]]:bg-emerald-500'
}

export function ApiKeyStatusCell({ row }: CellContext<ApiKey, unknown>) {
  const { t } = useTranslation()
  const statusConfig = API_KEY_STATUSES[row.getValue('status') as number]
  if (!statusConfig) return null
  return (
    <StatusBadge
      label={t(statusConfig.label)}
      variant={statusConfig.variant}
      copyable={false}
      className='-ml-1.5'
    />
  )
}

export function ApiKeyQuotaCell({ row }: CellContext<ApiKey, unknown>) {
  const { t } = useTranslation()
  const apiKey = row.original
  if (apiKey.unlimited_quota) {
    return <UnlimitedQuotaBadge used={apiKey.used_quota} />
  }

  const used = apiKey.used_quota
  const remaining = apiKey.remain_quota
  const total = used + remaining
  const percentage = total > 0 ? (remaining / total) * 100 : 0

  return (
    <Tooltip>
      <TooltipTrigger render={<div className='w-[150px] space-y-1' />}>
        <div className='flex justify-between text-xs'>
          <span className='font-medium tabular-nums'>
            {formatQuota(remaining)}
          </span>
          <span className='text-muted-foreground tabular-nums'>
            {formatQuota(total)}
          </span>
        </div>
        <Progress
          value={percentage}
          className={cn('h-1.5', getQuotaProgressColor(percentage))}
        />
      </TooltipTrigger>
      <TooltipContent>
        <div className='space-y-1 text-xs'>
          <div>
            {t('Used:')} {formatQuota(used)}
          </div>
          <div>
            {t('Remaining:')} {formatQuota(remaining)} ({percentage.toFixed(1)}
            %)
          </div>
          <div>
            {t('Total:')} {formatQuota(total)}
          </div>
        </div>
      </TooltipContent>
    </Tooltip>
  )
}

export function ApiKeyGroupCell({ row, column }: CellContext<ApiKey, unknown>) {
  const { t } = useTranslation()
  const { groupRatios } = column.columnDef.meta as ApiKeyGroupColumnMeta
  const apiKey = row.original
  const group = row.getValue('group') as string
  const ratio = group && group !== 'auto' ? groupRatios[group] : undefined

  if (group === 'auto') {
    return (
      <Tooltip>
        <TooltipTrigger render={<BadgeCell className='gap-1.5 text-xs' />}>
          <GroupBadge group='auto' />
          {apiKey.cross_group_retry && (
            <StatusBadge
              label={t('Cross-group')}
              variant='info'
              copyable={false}
            />
          )}
        </TooltipTrigger>
        <TooltipContent>
          <span className='text-xs'>
            {t(
              'Automatically selects the best available group with circuit breaker mechanism'
            )}
          </span>
        </TooltipContent>
      </Tooltip>
    )
  }
  return (
    <TruncatedCell
      className='-ml-1.5'
      tooltipContent={group || '-'}
      tooltipClassName='break-all'
    >
      <GroupBadge group={group} ratio={ratio} />
    </TruncatedCell>
  )
}

export function ApiKeyExpiryCell({
  row,
  column,
}: CellContext<ApiKey, unknown>) {
  const { t } = useTranslation()
  const { now, locale, justNowLabel } = column.columnDef
    .meta as ApiKeyTimeColumnMeta
  const expiredTime = row.getValue('expired_time') as number
  if (expiredTime === -1) {
    return (
      <StatusBadge
        label={t('Never')}
        variant='neutral'
        copyable={false}
        className='-ml-1.5'
      />
    )
  }
  const isExpired = expiredTime * 1000 < now
  return (
    <ApiKeyTimestampCell
      timestamp={expiredTime}
      now={now}
      locale={locale}
      justNowLabel={justNowLabel}
      className={cn(isExpired ? 'text-destructive' : 'text-muted-foreground')}
    />
  )
}
