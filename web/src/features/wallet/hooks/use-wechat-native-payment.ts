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
import i18next from 'i18next'
import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import {
  getWeChatNativeOrder,
  isApiSuccess,
  requestWeChatNativePayment,
} from '../api'
import type {
  TopupStatus,
  WeChatNativeOrder,
  WeChatNativeOrderResponse,
} from '../types'

const WECHAT_ORDER_POLL_INTERVAL_MS = 2000

export function isTerminalWeChatOrderStatus(status: TopupStatus): boolean {
  return status === 'success' || status === 'failed' || status === 'expired'
}

interface PollWeChatOrderOptions {
  tradeNo: string
  signal: AbortSignal
  fetchOrder?: (
    tradeNo: string,
    signal: AbortSignal
  ) => Promise<WeChatNativeOrderResponse>
  wait?: (signal: AbortSignal) => Promise<void>
  onUpdate: (order: WeChatNativeOrder) => void
}

function waitForNextPoll(signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve()
      return
    }

    const finish = () => {
      window.clearTimeout(timeout)
      signal.removeEventListener('abort', finish)
      resolve()
    }
    const timeout = window.setTimeout(finish, WECHAT_ORDER_POLL_INTERVAL_MS)
    signal.addEventListener('abort', finish, { once: true })
  })
}

export async function pollWeChatOrder({
  tradeNo,
  signal,
  fetchOrder = getWeChatNativeOrder,
  wait = waitForNextPoll,
  onUpdate,
}: PollWeChatOrderOptions): Promise<WeChatNativeOrder | undefined> {
  let latestOrder: WeChatNativeOrder | undefined

  while (!signal.aborted) {
    const response = await fetchOrder(tradeNo, signal)
    if (!response.data) {
      return latestOrder
    }

    latestOrder = response.data
    onUpdate(latestOrder)
    if (isTerminalWeChatOrderStatus(latestOrder.status) || signal.aborted) {
      return latestOrder
    }

    await wait(signal)
  }

  return latestOrder
}

function newIdempotencyKey(): string {
  if (typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }

  const bytes = crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80
  const hex = Array.from(bytes, (value) => value.toString(16).padStart(2, '0'))
  return `${hex.slice(0, 4).join('')}-${hex.slice(4, 6).join('')}-${hex.slice(6, 8).join('')}-${hex.slice(8, 10).join('')}-${hex.slice(10).join('')}`
}

interface UseWeChatNativePaymentOptions {
  onSuccess?: () => void | Promise<void>
}

export function useWeChatNativePayment({
  onSuccess,
}: UseWeChatNativePaymentOptions = {}) {
  const [order, setOrder] = useState<WeChatNativeOrder | null>(null)
  const [open, setOpen] = useState(false)
  const [processing, setProcessing] = useState(false)
  const [polling, setPolling] = useState(false)
  const actionRef = useRef<{ amount: string; key: string } | null>(null)
  const notifiedTradeRef = useRef<string | null>(null)

  const createPayment = useCallback(async (amount: string) => {
    let action = actionRef.current
    if (!action || action.amount !== amount) {
      action = { amount, key: newIdempotencyKey() }
      actionRef.current = action
    }

    setProcessing(true)
    try {
      const response = await requestWeChatNativePayment({
        amount,
        idempotency_key: action.key,
      })

      if (response.data) {
        setOrder(response.data)
        setOpen(true)
      }

      if (isApiSuccess(response)) {
        return true
      }

      if (response.data?.status === 'pending') {
        toast.warning(i18next.t('Payment status is being confirmed'))
        return true
      }

      toast.error(response.message || i18next.t('WeChat Pay request failed'))
      return false
    } catch {
      toast.error(i18next.t('WeChat Pay request failed'))
      return false
    } finally {
      setProcessing(false)
    }
  }, [])

  const orderTradeNo = order?.trade_no
  const orderStatus = order?.status

  useEffect(() => {
    if (!open || !orderTradeNo || orderStatus !== 'pending') {
      return
    }

    const controller = new AbortController()
    setPolling(true)
    void pollWeChatOrder({
      tradeNo: orderTradeNo,
      signal: controller.signal,
      onUpdate: (updatedOrder) => {
        setOrder((currentOrder) => ({
          ...updatedOrder,
          code_url:
            updatedOrder.code_url ||
            (currentOrder?.trade_no === updatedOrder.trade_no
              ? currentOrder.code_url
              : ''),
        }))
      },
    })
      .catch(() => {
        if (!controller.signal.aborted) {
          toast.error(i18next.t('WeChat Pay status check failed'))
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setPolling(false)
        }
      })

    return () => controller.abort()
  }, [open, orderStatus, orderTradeNo])

  useEffect(() => {
    if (!order || !isTerminalWeChatOrderStatus(order.status)) {
      return
    }

    actionRef.current = null
    if (
      order.status === 'success' &&
      notifiedTradeRef.current !== order.trade_no
    ) {
      notifiedTradeRef.current = order.trade_no
      toast.success(i18next.t('Payment successful'))
      void onSuccess?.()
    }
  }, [onSuccess, order])

  const retryPayment = useCallback(() => {
    if (!order) return Promise.resolve(false)
    return createPayment(order.amount)
  }, [createPayment, order])

  return {
    order,
    open,
    setOpen,
    processing,
    polling,
    createPayment,
    retryPayment,
  }
}
