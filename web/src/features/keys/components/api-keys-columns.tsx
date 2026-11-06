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
import type { ColumnDef } from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'

import { Checkbox } from '@/components/ui/checkbox'
import { toIntlLocale } from '@/i18n/languages'
import { getUserGroups } from '@/lib/api'
import dayjs from '@/lib/dayjs'

import type { ApiKey } from '../types'
import {
  ApiKeyStatusCell,
  ApiKeyQuotaCell,
  ApiKeyGroupCell,
  ApiKeyExpiryCell,
  type ApiKeyTimeColumnMeta,
} from './api-key-column-cells'
import { ApiKeyTimestampCell } from './api-key-timestamp-cell'
import {
  ApiKeyCell,
  IpRestrictionsCell,
  ModelLimitsCell,
} from './api-keys-cells'
import { DataTableRowActions } from './data-table-row-actions'

function useGroupRatios(): Record<string, number> {
  const { data } = useQuery({
    queryKey: ['user-groups'],
    queryFn: getUserGroups,
    staleTime: 0,
    select: (res) => {
      if (!res.success || !res.data) return {}
      const ratios: Record<string, number> = {}
      for (const [group, info] of Object.entries(res.data)) {
        if (typeof info.ratio === 'number') {
          ratios[group] = info.ratio
        }
      }
      return ratios
    },
  })

  return data ?? {}
}

// TanStack renders cell callbacks as React components. Keep their types stable
// when the clock, translations, or the key-resolution context updates.
const API_KEY_COLUMNS: ColumnDef<ApiKey>[] = [
  {
    id: 'select',
    header: ({ table }) => (
      <Checkbox
        checked={table.getIsAllPageRowsSelected()}
        indeterminate={table.getIsSomePageRowsSelected()}
        onCheckedChange={(value) => table.toggleAllPageRowsSelected(!!value)}
        aria-label='Select all'
        className='translate-y-[2px]'
      />
    ),
    cell: ({ row }) => (
      <Checkbox
        checked={row.getIsSelected()}
        onCheckedChange={(value) => row.toggleSelected(!!value)}
        aria-label='Select row'
        className='translate-y-[2px]'
      />
    ),
    enableSorting: false,
    enableHiding: false,
    size: 40,
  },
  {
    accessorKey: 'name',
    header: 'Name',
    cell: ({ row }) => (
      <span className='font-medium'>{row.getValue('name')}</span>
    ),
    size: 180,
    meta: { mobileTitle: true },
  },
  {
    accessorKey: 'status',
    header: 'Status',
    cell: ApiKeyStatusCell,
    filterFn: (row, id, value) => value.includes(String(row.getValue(id))),
    size: 120,
    meta: { mobileBadge: true },
  },
  {
    id: 'key',
    accessorKey: 'key',
    header: 'API Key',
    cell: ({ row }) => <ApiKeyCell apiKey={row.original} />,
    enableSorting: false,
    size: 260,
  },
  {
    id: 'quota',
    accessorKey: 'remain_quota',
    header: 'Quota',
    cell: ApiKeyQuotaCell,
    size: 170,
  },
  {
    accessorKey: 'group',
    header: 'Group',
    cell: ApiKeyGroupCell,
    size: 160,
    meta: { mobileHidden: true },
  },
  {
    id: 'model_limits',
    accessorKey: 'model_limits',
    header: 'Models',
    cell: ({ row }) => <ModelLimitsCell apiKey={row.original} />,
    enableSorting: false,
    size: 160,
    meta: { mobileHidden: true },
  },
  {
    id: 'allow_ips',
    accessorKey: 'allow_ips',
    header: 'IP Restriction',
    cell: ({ row }) => <IpRestrictionsCell apiKey={row.original} />,
    enableSorting: false,
    size: 160,
    meta: { mobileHidden: true },
  },
  {
    accessorKey: 'created_time',
    header: 'Created',
    cell: ({ row, column }) => {
      const { now, locale, justNowLabel } = column.columnDef
        .meta as ApiKeyTimeColumnMeta
      return (
        <ApiKeyTimestampCell
          timestamp={row.getValue('created_time')}
          now={now}
          locale={locale}
          justNowLabel={justNowLabel}
          className='text-muted-foreground'
        />
      )
    },
    size: 180,
    meta: { mobileHidden: true },
  },
  {
    accessorKey: 'accessed_time',
    header: 'Last Used',
    cell: ({ row, column }) => {
      const { now, locale, justNowLabel } = column.columnDef
        .meta as ApiKeyTimeColumnMeta
      const staleAccessThreshold = dayjs(now).subtract(3, 'month').valueOf()
      const accessedTime = row.getValue('accessed_time') as number
      const isStale =
        accessedTime > 0 && accessedTime * 1000 < staleAccessThreshold

      return (
        <ApiKeyTimestampCell
          timestamp={accessedTime}
          now={now}
          locale={locale}
          justNowLabel={justNowLabel}
          className={isStale ? 'text-warning' : 'text-muted-foreground'}
        />
      )
    },
    size: 180,
    meta: { mobileHidden: true },
  },
  {
    accessorKey: 'expired_time',
    header: 'Expires',
    cell: ApiKeyExpiryCell,
    size: 180,
    meta: { mobileHidden: true },
  },
  {
    id: 'actions',
    header: 'Actions',
    cell: ({ row }) => <DataTableRowActions row={row} />,
    meta: { pinned: 'right' as const },
  },
]

export function useApiKeysColumns(now: number): ColumnDef<ApiKey>[] {
  const { t, i18n } = useTranslation()
  const groupRatios = useGroupRatios()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const justNowLabel = t('Just now')

  return API_KEY_COLUMNS.map((column) => {
    const localized: ColumnDef<ApiKey> = {
      ...column,
      meta: { ...column.meta, now, locale, justNowLabel, groupRatios },
    }
    if (typeof column.header === 'string') {
      localized.header = t(column.header)
    }
    return localized
  })
}
