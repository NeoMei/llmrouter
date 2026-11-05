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
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuthIdentity } from '@/hooks/use-auth-identity'
import { formatLogQuota } from '@/lib/format'
import { cn } from '@/lib/utils'

import { getLogStats, getUserLogStats } from '../api'
import { buildApiParams } from '../lib/utils'
import { useLogsViewScope, useUsageLogsContext } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

function StatBadge(props: {
  label: string
  value: string | number
  accent: string
}) {
  return (
    <span className='border-border/60 bg-muted/25 inline-flex h-7 items-center gap-2 rounded-md border px-2.5 text-xs shadow-xs'>
      <span className={cn('h-3.5 w-0.5 rounded-full', props.accent)} />
      <span className='text-muted-foreground'>{props.label}</span>
      <span className='text-foreground/85 font-mono font-semibold tabular-nums'>
        {props.value}
      </span>
    </span>
  )
}

export function CommonLogsStats() {
  const { t } = useTranslation()
  const { isAdminView: isAdmin } = useLogsViewScope()
  const searchParams = route.useSearch()
  const { sensitiveVisible } = useUsageLogsContext()

  const identity = useAuthIdentity()
  const {
    data: stats,
    isPending,
    isError,
    isFetching,
    refetch,
  } = useQuery({
    queryKey: ['usage-logs-stats', identity, isAdmin, searchParams],
    queryFn: async () => {
      const params = buildApiParams({
        page: 1,
        pageSize: 1,
        searchParams,
        columnFilters: [],
        isAdmin,
      })

      const result = isAdmin
        ? await getLogStats(params)
        : await getUserLogStats(params)

      if (
        !result.success ||
        !result.data ||
        ![result.data.quota, result.data.rpm, result.data.tpm].every(
          Number.isFinite
        )
      ) {
        throw new Error('Usage statistics unavailable')
      }
      return result.data
    },
    retry: false,
    gcTime: 0,
  })

  if (isPending) {
    return (
      <div className='flex items-center gap-2'>
        <Skeleton className='h-7 w-[150px] rounded-md' />
        <Skeleton className='h-7 w-[100px] rounded-md' />
        <Skeleton className='h-7 w-[120px] rounded-md' />
      </div>
    )
  }

  return (
    <div className='flex flex-wrap items-center gap-2'>
      {isError ? (
        <p role='alert' className='text-destructive text-sm'>
          {stats
            ? t('Showing previous statistics. Refresh failed.')
            : t('Usage statistics unavailable')}
        </p>
      ) : null}
      <Button
        size='sm'
        variant='outline'
        disabled={isFetching}
        onClick={() => void refetch()}
      >
        {isError ? t('Retry statistics') : t('Refresh statistics')}
      </Button>
      {stats ? (
        <>
          <StatBadge
            label={t('LLMRouter product usage')}
            value={sensitiveVisible ? formatLogQuota(stats.quota) : '••••'}
            accent='bg-sky-500/70'
          />
          <StatBadge
            label={t('RPM')}
            value={stats.rpm}
            accent='bg-rose-500/65'
          />
          <StatBadge
            label={t('TPM')}
            value={stats.tpm}
            accent='bg-slate-400/70'
          />
        </>
      ) : null}
    </div>
  )
}
