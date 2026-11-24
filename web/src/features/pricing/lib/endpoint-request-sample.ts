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
import { replaceModelInPath } from './model-helpers'

type EndpointRequest = {
  baseUrl: string
  apiKeyEnv: string
  modelName: string
  endpointType: string
  endpointPath: string
  endpointMethod: string
}

const SDK_ENDPOINT_PATHS: Record<string, string> = {
  openai: '/v1/chat/completions',
  'openai-response': '/v1/responses',
  anthropic: '/v1/messages',
  gemini: '/v1beta/models/{model}:generateContent',
  embeddings: '/v1/embeddings',
  'image-generation': '/v1/images/generations',
}

export function usesDirectEndpointSample(request: EndpointRequest): boolean {
  const sdkPath = Object.hasOwn(SDK_ENDPOINT_PATHS, request.endpointType)
    ? SDK_ENDPOINT_PATHS[request.endpointType]
    : undefined
  return (
    request.endpointMethod !== 'POST' ||
    !sdkPath ||
    request.endpointPath !== replaceModelInPath(sdkPath, request.modelName)
  )
}

function sampleBody(endpointType: string, model: string): object {
  const text = 'Explain quantum entanglement in one paragraph.'
  const messages = [{ role: 'user', content: text }]
  switch (endpointType) {
    case 'anthropic':
      return { model, max_tokens: 1024, messages }
    case 'gemini':
      return { contents: [{ parts: [{ text }] }] }
    case 'openai-response':
      return { model, input: text }
    case 'embeddings':
      return { model, input: 'The food was delicious.' }
    case 'jina-rerank':
      return {
        model,
        query: 'What is quantum entanglement?',
        documents: [text],
      }
    case 'image-generation':
      return {
        model,
        prompt: 'A serene koi pond at sunset.',
        size: '1024x1024',
        n: 1,
      }
    case 'openai-video':
      return { model, prompt: 'Ripples on a koi pond at sunset.' }
    default:
      return { model, messages }
  }
}

function quoteShellArgument(value: string): string {
  return `'${value.replaceAll("'", "'\\''")}'`
}

export function buildEndpointRequestSample(
  language: 'curl' | 'python' | 'typescript' | 'javascript',
  request: EndpointRequest
): string {
  const url = request.baseUrl + request.endpointPath
  const { endpointMethod: method, endpointType, apiKeyEnv } = request
  const hasBody = method !== 'GET' && method !== 'HEAD'
  const headers: Record<string, string> = {}
  if (hasBody) headers['Content-Type'] = 'application/json'
  let authHeader = 'Authorization'
  let authPrefix = 'Bearer '
  if (endpointType === 'anthropic' || endpointType === 'gemini') {
    authHeader = endpointType === 'anthropic' ? 'x-api-key' : 'x-goog-api-key'
    authPrefix = ''
  }
  if (endpointType === 'anthropic') headers['anthropic-version'] = '2023-06-01'
  const body = hasBody
    ? JSON.stringify(sampleBody(endpointType, request.modelName), null, 2)
    : undefined

  if (language === 'curl') {
    return [
      `curl --request ${quoteShellArgument(method)} ${quoteShellArgument(url)}`,
      `  -H "${authHeader}: ${authPrefix}$${apiKeyEnv}"`,
      ...Object.entries(headers).map(
        ([key, value]) => `  -H ${quoteShellArgument(`${key}: ${value}`)}`
      ),
      ...(body ? [`  --data ${quoteShellArgument(body)}`] : []),
    ].join(' \\' + '\n')
  }

  if (language === 'python') {
    const pythonHeaders = {
      ...headers,
      [authHeader]: `${authPrefix}<YOUR_API_KEY>`,
    }
    return [
      'import requests',
      '',
      'response = requests.request(',
      `    ${JSON.stringify(method)},`,
      `    ${JSON.stringify(url)},`,
      `    headers=${JSON.stringify(pythonHeaders)},`,
      ...(body ? [`    data=${JSON.stringify(body)},`] : []),
      ')',
      'print(response.text)',
    ].join('\n')
  }

  const fetchHeaders = Object.entries(headers).map(
    ([key, value]) => `    ${JSON.stringify(key)}: ${JSON.stringify(value)},`
  )
  const fetchAuthValue = authPrefix
    ? `${JSON.stringify(authPrefix)} + process.env.${apiKeyEnv}`
    : `process.env.${apiKeyEnv}`
  return [
    `const response = await fetch(${JSON.stringify(url)}, {`,
    `  method: ${JSON.stringify(method)},`,
    '  headers: {',
    `    ${JSON.stringify(authHeader)}: ${fetchAuthValue},`,
    ...fetchHeaders,
    '  },',
    ...(body
      ? [`  body: JSON.stringify(${body.replaceAll('\n', '\n  ')}),`]
      : []),
    '})',
    'console.log(await response.text())',
  ].join('\n')
}
