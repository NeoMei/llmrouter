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
export type IframePolicyPreset = {
  allowScripts?: boolean
  allowForms?: boolean
  allowPopups?: boolean
  allowDownloads?: boolean
  allowPresentation?: boolean
  allowCamera?: boolean
  allowMicrophone?: boolean
}

export type IframePolicyEnvironment = {
  production: boolean
  trustedPreviewOrigin?: string
}

export type IframePolicy = {
  allowed: boolean
  url?: string
  sandbox: string
  allow: string
  reason?: string
}

const LOCAL_HOSTS = new Set(['localhost', '127.0.0.1', '[::1]', '::1'])

export function resolveIframePolicy(
  rawUrl: string,
  preset: IframePolicyPreset = {},
  environment: IframePolicyEnvironment
): IframePolicy {
  let target: URL
  try {
    target = new URL(rawUrl)
  } catch {
    return denied('Invalid preview URL')
  }

  const isLocalDevelopment =
    !environment.production &&
    target.protocol === 'http:' &&
    LOCAL_HOSTS.has(target.hostname)
  if (target.protocol !== 'https:' && !isLocalDevelopment) {
    return denied('Only HTTPS preview URLs are allowed')
  }

  const sandbox = new Set<string>()
  if (preset.allowScripts !== false) sandbox.add('allow-scripts')
  if (preset.allowForms) sandbox.add('allow-forms')
  if (preset.allowPopups) sandbox.add('allow-popups')
  if (preset.allowDownloads) sandbox.add('allow-downloads')
  if (preset.allowPresentation) sandbox.add('allow-presentation')

  const trustedOrigin = normalizeOrigin(environment.trustedPreviewOrigin)
  if (trustedOrigin && trustedOrigin === target.origin) {
    sandbox.add('allow-scripts')
    sandbox.add('allow-same-origin')
  }

  const allow: string[] = []
  if (preset.allowCamera) allow.push('camera')
  if (preset.allowMicrophone) allow.push('microphone')

  return {
    allowed: true,
    url: target.toString(),
    sandbox: [...sandbox].join(' '),
    allow: allow.join('; '),
  }
}

function normalizeOrigin(value?: string): string | undefined {
  if (!value) return undefined
  try {
    const url = new URL(value)
    if (url.protocol !== 'https:') return undefined
    return url.origin
  } catch {
    return undefined
  }
}

function denied(reason: string): IframePolicy {
  return { allowed: false, sandbox: '', allow: '', reason }
}
