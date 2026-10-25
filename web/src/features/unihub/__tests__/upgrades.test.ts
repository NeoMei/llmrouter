import assert from 'node:assert/strict'
import { test } from 'node:test'

import type { Offer, Subscription } from '../sdk'
import { upgradeSubscription } from '../upgrades'

const current: Offer = {
  id: 'current',
  product_id: 'llmrouter',
  name: 'Current',
  account_kind: 'both',
  kind: 'membership',
  period: 'month',
  price: '1000000',
  monthly_credit: '1000000',
  entitlements: {},
  rank: 2,
  validity_months: 1,
  upgrade_rule: 'remaining_term_prorata_v1',
  refund_rule: 'unused_only_v1',
  status: 'published',
  sandbox: true,
}
const subscription: Subscription = {
  id: 'subscription',
  account_id: 'account',
  product_id: 'llmrouter',
  offer: current,
  version: 1,
  anchor: '2026-01-01',
  paid_through: '2027-01-01',
  status: 'active',
}
test('higher rank same-period membership selects matching active product subscription', () => {
  assert.equal(
    upgradeSubscription([subscription], { ...current, id: 'higher', rank: 3 })
      ?.id,
    'subscription'
  )
})
test('current lower annual addon other-product and inactive offers cannot be upgrade choices', () => {
  for (const offer of [
    current,
    { ...current, rank: 1 },
    { ...current, rank: 3, period: 'year' },
    { ...current, rank: 3, kind: 'addon' },
    { ...current, rank: 3, product_id: 'other' },
  ]) {
    assert.equal(upgradeSubscription([subscription], offer), undefined)
  }
  assert.equal(
    upgradeSubscription([{ ...subscription, status: 'expired' }], {
      ...current,
      rank: 3,
    }),
    undefined
  )
})
