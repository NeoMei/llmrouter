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
export type Capability = {
  channel_type: number
  channel_name: string
  protocol: string
  operation: string
  supported: boolean
  reason?: string
}

export type CapabilitySnapshot = {
  version: string
  capabilities: Capability[]
}

export function hasCapability(
  snapshot: CapabilitySnapshot | undefined,
  operation: string
): boolean {
  return Boolean(
    snapshot?.capabilities.some(
      (capability) => capability.operation === operation && capability.supported
    )
  )
}

export function getSupportingCapabilities(
  snapshot: CapabilitySnapshot | undefined,
  operation: string
): Capability[] {
  return (snapshot?.capabilities ?? [])
    .filter(
      (capability) => capability.operation === operation && capability.supported
    )
    .sort(
      (left, right) =>
        left.channel_name.localeCompare(right.channel_name) ||
        left.protocol.localeCompare(right.protocol)
    )
}
