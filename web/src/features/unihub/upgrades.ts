import type { Offer, Subscription } from './sdk'

/** Match the existing subscription whose period/rank permits this offer upgrade. */
export function upgradeSubscription(
  subscriptions: Subscription[],
  offer: Offer
): Subscription | undefined {
  if (offer.kind !== 'membership') return undefined
  return subscriptions.find(
    (subscription) =>
      subscription.status === 'active' &&
      subscription.product_id === offer.product_id &&
      subscription.offer.kind === 'membership' &&
      subscription.offer.period === offer.period &&
      offer.rank > subscription.offer.rank
  )
}
