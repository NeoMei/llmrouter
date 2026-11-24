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
import assert from 'node:assert/strict'
import test from 'node:test'

import {
  buildEndpointRequestSample,
  usesDirectEndpointSample,
} from '../lib/endpoint-request-sample'
import { getPricingModelEndpoints } from '../lib/model-helpers'

const defaultEndpoints = {
  openai: { path: '/v1/chat/completions', method: 'POST' },
  gemini: { path: '/v1beta/models/{model}:generateContent' },
}

test('pricing examples resolve the model-specific path and HTTP method', () => {
  const model = {
    model_name: 'model-a',
    supported_endpoint_types: ['gemini', 'openai'],
    endpoint_info: {
      gemini: { path: '/custom/{model}:generate', method: 'POST' },
      openai: { path: '/custom/chat', method: 'GET' },
    },
  }
  assert.deepEqual(getPricingModelEndpoints(model, defaultEndpoints), [
    { type: 'gemini', path: '/custom/model-a:generate', method: 'POST' },
    { type: 'openai', path: '/custom/chat', method: 'GET' },
  ])
})

test('a model endpoint map never fills missing types from the shared map', () => {
  const model = {
    model_name: 'model-b',
    supported_endpoint_types: ['openai', 'gemini'],
    endpoint_info: { gemini: { path: '/custom/gemini', method: 'POST' } },
  }
  assert.deepEqual(getPricingModelEndpoints(model, defaultEndpoints), [
    { type: 'gemini', path: '/custom/gemini', method: 'POST' },
  ])
})

test('pricing responses without per-model endpoint data retain the legacy map', () => {
  const model = {
    model_name: 'model-c',
    supported_endpoint_types: ['gemini'],
  }
  assert.deepEqual(getPricingModelEndpoints(model, defaultEndpoints), [
    {
      type: 'gemini',
      path: '/v1beta/models/model-c:generateContent',
      method: 'POST',
    },
  ])
})

test('custom GET endpoints use direct samples with their path and no request body', () => {
  const request = {
    baseUrl: 'https://api.example.com',
    apiKeyEnv: 'NEW_API_KEY',
    modelName: 'model-a',
    endpointType: 'openai',
    endpointPath: '/custom/models/model-a',
    endpointMethod: 'GET',
  }

  assert.equal(usesDirectEndpointSample(request), true)
  assert.match(
    buildEndpointRequestSample('curl', request),
    /^curl --request 'GET' 'https:\/\/api\.example\.com\/custom\/models\/model-a'/
  )
  const python = buildEndpointRequestSample('python', request)
  assert.match(python, /"GET"/)
  assert.match(python, /\/custom\/models\/model-a/)
  assert.doesNotMatch(python, /data=/)
  const typescript = buildEndpointRequestSample('typescript', request)
  assert.match(typescript, /method: "GET"/)
  assert.doesNotMatch(typescript, /body:/)
})

test('video examples send a prompt to the video endpoint', () => {
  const request = {
    baseUrl: 'https://api.example.com',
    apiKeyEnv: 'NEW_API_KEY',
    modelName: 'seedance',
    endpointType: 'openai-video',
    endpointPath: '/v1/videos',
    endpointMethod: 'POST',
  }
  assert.equal(usesDirectEndpointSample(request), true)
  const code = buildEndpointRequestSample('curl', request)
  assert.match(code, /"prompt":/)
  assert.doesNotMatch(code, /"messages":/)
})
