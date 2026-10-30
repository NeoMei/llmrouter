/* Copyright (C) 2023-2026 QuantumNous. Licensed under AGPL-3.0-or-later. */
import { useQuery } from '@tanstack/react-query'

import { getFreshAuthHeaders } from '@/lib/auth-session'

import type { FundingMode } from './sdk'
import type { PanelProps } from './ui'

type Model = { id: string; policy?: string; rate_ids: Record<string, string> }
type Technical = {
  product_id: string
  instance_id: string
  admission_mode: 'metered_v2' | 'legacy'
  models: Model[]
}
type Rate = {
  id: string
  model: string
  status: string
  asset: string
  semantics: string
  input?: { numerator?: string; denominator?: string }
  output?: { numerator?: string; denominator?: string }
  cache_read?: { numerator?: string; denominator?: string }
  cache_write?: { numerator?: string; denominator?: string }
  reasoning?: { numerator?: string; denominator?: string }
  metered?: {
    version?: string
    operation: string
    policy?: string
    terms?: {
      key?: string
      unit?: string
      provider_unit?: string
      step?: unknown
      minimum?: unknown
      price?: { numerator?: string; denominator?: string }
    }[]
  }
}
type Consistency = {
  product_id: string
  asset: string
  allowlist: string[]
  published_rates: Rate[]
  duplicate_models: string[]
}

// Mirrors gateway requestBound: the legacy path can reserve only a single
// bounded per-request metered tariff; token and mixed tariffs are rejected.
function legacyRequestRate(rate: Rate): boolean {
  const tariff = rate.metered
  if (
    rate.semantics !== 'metered_v2' ||
    !tariff ||
    tariff.version !== 'metered_v2' ||
    tariff.operation !== 'chat' ||
    tariff.policy !== 'sum' ||
    !Array.isArray(tariff.terms) ||
    tariff.terms.length !== 1
  ) {
    return false
  }
  if (
    [
      rate.input,
      rate.output,
      rate.cache_read,
      rate.cache_write,
      rate.reasoning,
    ].some((value) => value && (value.numerator || value.denominator))
  ) {
    return false
  }
  const term = tariff.terms[0]
  const numerator = term.price?.numerator
  const denominator = term.price?.denominator
  if (
    term.key !== 'requests' ||
    term.unit !== 'request' ||
    term.provider_unit ||
    term.step != null ||
    term.minimum != null ||
    typeof numerator !== 'string' ||
    typeof denominator !== 'string' ||
    !/^(0|[1-9][0-9]*)$/.test(numerator) ||
    !/^(0|[1-9][0-9]*)$/.test(denominator) ||
    numerator.length > 38 ||
    denominator.length > 38 ||
    denominator === '0'
  ) {
    return false
  }
  const n = BigInt(numerator),
    d = BigInt(denominator)
  const amount = (n + d - 1n) / d
  return amount <= 9223372036854775807n
}

export function useChatModels(props: PanelProps, funding: FundingMode | '') {
  const query = useQuery({
    queryKey: [
      'chat-capabilities',
      props.session.epoch,
      props.account.id,
      props.config.product_id,
      props.config.instance_id,
      funding,
    ],
    enabled: !!funding,
    queryFn: () =>
      props.session.bound(async () => {
        const headers = await getFreshAuthHeaders()
        const [response, consistency] = await Promise.all([
          fetch('/api/unihub/chat-models', {
            headers,
            signal: props.session.signal,
            credentials: 'same-origin',
            cache: 'no-store',
          }),
          props.session.client.request<Consistency>(
            'GET',
            `/v1/products/${encodeURIComponent(props.config.product_id)}/model-consistency?${new URLSearchParams({ account_id: props.account.id, funding_mode: funding }).toString()}`
          ),
        ])
        if (!response.ok) throw new Error('UniHub configuration unavailable')
        const technical = (await response.json()) as Technical
        if (
          technical.product_id !== props.config.product_id ||
          technical.instance_id !== (props.config.instance_id ?? 'default') ||
          consistency.product_id !== props.config.product_id ||
          !['metered_v2', 'legacy'].includes(technical.admission_mode) ||
          !Array.isArray(technical.models) ||
          !Array.isArray(consistency.allowlist) ||
          !Array.isArray(consistency.published_rates) ||
          !Array.isArray(consistency.duplicate_models) ||
          consistency.asset !==
            (funding === 'prepaid_cash' ? 'CNY/6' : 'CREDIT/6')
        ) {
          throw new Error('UniHub configuration unavailable')
        }
        const models = technical.models
          .filter((item) => {
            const rateId = item.rate_ids?.[consistency.asset]
            const rates = consistency.published_rates.filter(
              (rate) =>
                rate.model === item.id &&
                rate.status === 'published' &&
                (rate.asset || 'CREDIT/6') === consistency.asset &&
                (technical.admission_mode === 'metered_v2'
                  ? rate.semantics === 'metered_v2' &&
                    rate.metered?.operation === 'chat' &&
                    typeof item.policy === 'string' &&
                    item.policy !== '' &&
                    rate.metered?.policy === item.policy
                  : (rate.semantics === 'exclusive_v1' && !rate.metered) ||
                    legacyRequestRate(rate))
            )
            return (
              typeof item.id === 'string' &&
              props.config.models.includes(item.id) &&
              consistency.allowlist.includes(item.id) &&
              !consistency.duplicate_models.includes(item.id) &&
              rates.length === 1 &&
              (!rateId || rates[0].id === rateId)
            )
          })
          .map((item) => item.id)
        return { models, asset: consistency.asset }
      }),
    retry: false,
  })
  return { ...query, data: query.isError ? undefined : query.data }
}
