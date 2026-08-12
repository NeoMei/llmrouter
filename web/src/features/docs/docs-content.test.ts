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
import { describe, test } from 'node:test'

import {
  DOCS_LANGUAGES,
  docsContent,
  type DocsContent,
  type DocsLanguage,
} from './docs-content'

const sectionIds = [
  'start',
  'get-token',
  'connection-address',
  'choose-model',
  'configure-agent',
  'verify',
  'troubleshooting',
]

function publicText(content: DocsContent) {
  return JSON.stringify(content).toLowerCase()
}

describe('LLMRouter connection guide content', () => {
  test('provides concise Chinese and English content with exactly seven sections', () => {
    assert.deepEqual(DOCS_LANGUAGES, ['zh-CN', 'en'])

    for (const language of DOCS_LANGUAGES) {
      const content = docsContent[language as DocsLanguage]
      assert.equal(content.sections.length, 7)
      assert.deepEqual(
        content.sections.map((section) => section.id),
        sectionIds
      )
      assert.ok(content.title.length > 0)
      assert.ok(content.introduction.length > 0)
    }
  })

  test('covers the supported Agents and labels Claude Desktop as MCP tool access', () => {
    for (const language of DOCS_LANGUAGES) {
      const agents = docsContent[language].sections.find(
        (section) => section.id === 'configure-agent'
      )?.agents

      assert.ok(agents)
      assert.deepEqual(
        agents.map((agent) => agent.name),
        [
          'Codex CLI/Desktop',
          'Claude Code/Desktop',
          'OpenCode',
          'Continue',
          'Aider',
        ]
      )
      assert.match(
        agents.find((agent) => agent.name === 'Claude Code/Desktop')?.detail ??
          '',
        /Claude Desktop.*MCP.*tool access/i
      )
    }
  })

  test('links users to the current dashboard and installation destinations', () => {
    const expectedLinks = [
      '/dashboard/tokens',
      '/dashboard/models',
      '/client-model-routes',
      '/install/',
    ]

    for (const language of DOCS_LANGUAGES) {
      const links = docsContent[language].sections.flatMap(
        (section) => section.links
      )
      assert.deepEqual(
        links.map((link) => link.href).sort(),
        expectedLinks.sort()
      )
    }
  })

  test('keeps public copy limited to the Agent connection scope and LLMRouter identity', () => {
    const forbiddenScope = [
      /llmapi|new api/i,
      /sdk/,
      /protocol/,
      /streaming/,
      /tool calling/,
      /image input/,
      /reasoning internals/,
      /prices?/,
      /quotas?/,
      /静态模型/,
      /价格/,
      /配额/,
      /流式/,
      /工具调用/,
      /图像输入/,
      /推理(?:原理|内部)/,
      /迁移(?:历史)?/,
    ]

    for (const language of DOCS_LANGUAGES) {
      const content = publicText(docsContent[language])
      assert.match(content, /llmrouter/)
      assert.match(content, /llmrouter\.quukk\.com/)
      for (const pattern of forbiddenScope) {
        assert.doesNotMatch(content, pattern)
      }
    }
  })
})
