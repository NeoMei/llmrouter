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
