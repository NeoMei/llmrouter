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
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
  type ColumnDef,
} from '@tanstack/react-table'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { LongText } from '@/components/long-text'
import { StatusBadge } from '@/components/status-badge'
import { TableId } from '@/components/table-id'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useAuthStore } from '@/stores/auth-store'

import { getUsers, searchUsers } from '../api'
import { USER_ROLES, USER_STATUSES } from '../constants'
import type { User } from '../types'

type CentralUserRow = Pick<
  User,
  'id' | 'username' | 'display_name' | 'email' | 'role' | 'status'
>

/** UniHub owns this directory, so the table is read-only and shows no native fields. */
export function CentralUsersTable() {
  const { t } = useTranslation()
  const identitySource = useAuthStore(
    (state) => state.auth.user?.identity_source
  )
  const [keyword, setKeyword] = useState('')
  const [submitted, setSubmitted] = useState('')
  const [page, setPage] = useState(1)
  const pageSize = 20

  const { data, isLoading, isFetching } = useQuery({
    queryKey: ['central-users', identitySource, submitted, page],
    queryFn: async () => {
      const result = submitted
        ? await searchUsers({
            keyword: submitted,
            p: page,
            page_size: pageSize,
          })
        : await getUsers({ p: page, page_size: pageSize })
      if (!result.success) {
        throw new Error(result.message || 'Failed to load users')
      }
      return result.data ?? { items: [], total: 0 }
    },
  })

  const columns = useMemo<ColumnDef<CentralUserRow>[]>(
    () => [
      {
        accessorKey: 'id',
        header: t('ID'),
        cell: ({ row }) => (
          <TableId value={row.getValue('id') as string} className='text-sm' />
        ),
      },
      {
        accessorKey: 'username',
        header: t('Username'),
        cell: ({ row }) => (
          <LongText className='max-w-[220px] font-medium'>
            {row.getValue('username') as string}
          </LongText>
        ),
      },
      {
        accessorKey: 'email',
        header: t('Email'),
        cell: ({ row }) => (
          <LongText className='text-muted-foreground max-w-[240px] text-sm'>
            {(row.getValue('email') as string) || '-'}
          </LongText>
        ),
      },
      {
        accessorKey: 'status',
        header: t('Status'),
        cell: ({ row }) => {
          const status =
            USER_STATUSES[row.getValue('status') as keyof typeof USER_STATUSES]
          return status ? (
            <StatusBadge
              label={t(status.labelKey)}
              variant={status.variant}
              copyable={false}
            />
          ) : null
        },
      },
      {
        accessorKey: 'role',
        header: t('Role'),
        cell: ({ row }) => {
          const role =
            USER_ROLES[row.getValue('role') as keyof typeof USER_ROLES]
          return role ? (
            <span className='text-sm'>{t(role.labelKey)}</span>
          ) : null
        },
      },
    ],
    [t]
  )

  const table = useReactTable({
    data: (data?.items ?? []) as unknown as CentralUserRow[],
    columns,
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
    pageCount: Math.max(1, Math.ceil((data?.total ?? 0) / pageSize)),
  })

  const total = data?.total ?? 0
  const maxPage = Math.max(1, Math.ceil(total / pageSize))

  return (
    <div className='space-y-4'>
      <div className='flex flex-wrap items-center gap-2'>
        <Input
          aria-label={t('Filter by username, name or email...')}
          placeholder={t('Filter by username, name or email...')}
          value={keyword}
          onChange={(event) => setKeyword(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              setPage(1)
              setSubmitted(keyword.trim())
            }
          }}
          className='max-w-sm'
        />
        <Button
          variant='outline'
          onClick={() => {
            setPage(1)
            setSubmitted(keyword.trim())
          }}
        >
          {t('Search')}
        </Button>
      </div>
      <div className='rounded-md border'>
        <Table>
          <TableHeader>
            {table.getHeaderGroups().map((group) => (
              <TableRow key={group.id}>
                {group.headers.map((header) => (
                  <TableHead key={header.id}>
                    {flexRender(
                      header.column.columnDef.header,
                      header.getContext()
                    )}
                  </TableHead>
                ))}
              </TableRow>
            ))}
          </TableHeader>
          <TableBody>
            {isLoading || isFetching ? (
              <TableRow>
                <TableCell colSpan={columns.length}>{t('Loading')}</TableCell>
              </TableRow>
            ) : table.getRowModel().rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={columns.length}>
                  {t('No Users Found')}
                </TableCell>
              </TableRow>
            ) : (
              table.getRowModel().rows.map((row) => (
                <TableRow key={row.original.id}>
                  {row.getVisibleCells().map((cell) => (
                    <TableCell key={cell.id}>
                      {flexRender(
                        cell.column.columnDef.cell,
                        cell.getContext()
                      )}
                    </TableCell>
                  ))}
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
      <div className='text-muted-foreground flex items-center justify-end gap-3 text-sm'>
        <span>{total}</span>
        <Button
          variant='outline'
          size='sm'
          disabled={page <= 1}
          onClick={() => setPage((current) => current - 1)}
        >
          {t('Previous')}
        </Button>
        <span>{page}</span>
        <Button
          variant='outline'
          size='sm'
          disabled={page >= maxPage}
          onClick={() => setPage((current) => current + 1)}
        >
          {t('Next')}
        </Button>
      </div>
    </div>
  )
}
