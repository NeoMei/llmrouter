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
import i18next from 'i18next'
import { useState, useCallback } from 'react'
import { toast } from 'sonner'

import { useIsAdmin } from '@/hooks/use-admin'
import { useAuthIdentity } from '@/hooks/use-auth-identity'
import { useAuthStore } from '@/stores/auth-store'

import {
  getUserBillingHistory,
  getAllBillingHistory,
  completeOrder,
  isApiSuccess,
} from '../api'

// ============================================================================
// Billing History Hook
// ============================================================================

interface UseBillingHistoryOptions {
  enabled?: boolean
  unifiedCash?: boolean
  fundingResolved?: boolean
  /** Initial page number */
  initialPage?: number
  /** Initial page size */
  initialPageSize?: number
}

export function useBillingHistory(options: UseBillingHistoryOptions = {}) {
  const { initialPage = 1, initialPageSize = 10 } = options
  const administrator = useIsAdmin()
  const unifiedCash = useAuthStore(
    (state) => state.auth.user?.funding?.mode === 'unified_cash'
  )
  const identity = useAuthIdentity()
  const isAdmin =
    administrator &&
    options.fundingResolved !== false &&
    !(options.unifiedCash || unifiedCash)

  const [page, setPage] = useState(initialPage)
  const [pageSize, setPageSize] = useState(initialPageSize)
  const [keyword, setKeyword] = useState('')
  const [completing, setCompleting] = useState(false)

  const history = useQuery({
    queryKey: ['billing-history', identity, isAdmin, page, pageSize, keyword],
    queryFn: async () => {
      const response = isAdmin
        ? await getAllBillingHistory(page, pageSize, keyword)
        : await getUserBillingHistory(page, pageSize, keyword)
      if (
        !isApiSuccess(response) ||
        !response.data ||
        !Array.isArray(response.data.items) ||
        !Number.isSafeInteger(response.data.total) ||
        response.data.total < 0
      ) {
        throw new Error(i18next.t('Failed to load billing history'))
      }
      return response.data
    },
    enabled: options.enabled !== false,
    retry: false,
    gcTime: 0,
    staleTime: 0,
  })
  const fetchBillingHistory = history.refetch
  const records = history.data ? history.data.items : []
  const total = history.data ? history.data.total : 0
  const loading = history.isPending || history.isFetching

  /**
   * Complete a pending order (admin only)
   */
  const handleCompleteOrder = useCallback(
    async (tradeNo: string) => {
      if (!isAdmin || !history.isSuccess || history.isFetching) {
        toast.error(i18next.t('Admin access required'))
        return false
      }

      setCompleting(true)
      try {
        const response = await completeOrder({ trade_no: tradeNo })
        if (isApiSuccess(response)) {
          toast.success(i18next.t('Order completed successfully'))
          // Refresh the list
          await fetchBillingHistory()
          return true
        } else {
          toast.error(response.message || i18next.t('Failed to complete order'))
          return false
        }
      } catch (error) {
        // eslint-disable-next-line no-console
        console.error('Failed to complete order:', error)
        toast.error(i18next.t('Failed to complete order'))
        return false
      } finally {
        setCompleting(false)
      }
    },
    [isAdmin, fetchBillingHistory, history.isSuccess, history.isFetching]
  )

  /**
   * Change page
   */
  const handlePageChange = useCallback((newPage: number) => {
    setPage(newPage)
  }, [])

  /**
   * Change page size
   */
  const handlePageSizeChange = useCallback((newPageSize: number) => {
    setPageSize(newPageSize)
    setPage(1) // Reset to first page when changing page size
  }, [])

  /**
   * Search by keyword
   */
  const handleSearch = useCallback((newKeyword: string) => {
    setKeyword(newKeyword)
    setPage(1) // Reset to first page when searching
  }, [])

  return {
    records,
    total,
    page,
    pageSize,
    keyword,
    loading,
    completing,
    error: history.isError,
    hasData: history.data !== undefined,
    isAdmin,
    handlePageChange,
    handlePageSizeChange,
    handleSearch,
    handleCompleteOrder,
    refresh: fetchBillingHistory,
  }
}
