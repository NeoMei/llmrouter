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
import { z } from 'zod'

const canonicalQuantity = z.string().regex(/^(0|[1-9][0-9]*)$/)

export const unifiedCashFundingSchema = z.object({
  mode: z.literal('unified_cash'),
  asset: z.literal('CNY/6'),
  unit_version: z.literal('lr-cny-v1'),
  available: canonicalQuantity,
  held: canonicalQuantity,
  consumed: canonicalQuantity,
})

export type UnifiedCashFunding = z.infer<typeof unifiedCashFundingSchema>

export function exactAmount(value: string, asset: string): string {
  if (!/^(0|[1-9][0-9]*)$/.test(value)) {
    throw new TypeError('Invalid canonical quantity')
  }
  const padded = value.padStart(7, '0')
  return `${padded.slice(0, -6)}.${padded.slice(-6)} ${asset.split('/')[0]}`
}
