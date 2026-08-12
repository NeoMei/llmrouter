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
import { readFile } from 'node:fs/promises'
import { describe, test } from 'node:test'

import { docsContent } from './docs-content'

const docsPagePath = new URL('./docs-page.tsx', import.meta.url)
const docsIndexPath = new URL('./index.ts', import.meta.url)
const docsRoutePath = new URL('../../routes/docs.tsx', import.meta.url)

async function readSource(path: URL) {
  try {
    return await readFile(path, 'utf8')
  } catch (error: unknown) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return ''
    throw error
  }
}

describe('LLMRouter docs page source contract', () => {
  test('registers the public /docs route and exports DocsPage', async () => {
    const [indexSource, routeSource] = await Promise.all([
      readSource(docsIndexPath),
      readSource(docsRoutePath),
    ])

    assert.match(indexSource, /export\s*\{\s*DocsPage\s*\}/)
    assert.match(routeSource, /createFileRoute\('\/docs'\)/)
    assert.match(routeSource, /DocsPage/)
  })

  test('renders the seven content anchors in PublicLayout with compact navigation', async () => {
    const source = await readSource(docsPagePath)
    const sectionIds = docsContent.en.sections.map((section) => section.id)

    assert.equal(sectionIds.length, 7)
    assert.equal(new Set(sectionIds).size, 7)
    assert.match(source, /<PublicLayout/)
    assert.match(source, /id=\{section\.id\}/)
    assert.match(source, /content\.sections\.map/)
    assert.match(source, /<nav\b/)
    assert.match(source, /overflow-x-auto/)
  })

  test('uses only the four approved internal destinations and keeps public naming', async () => {
    const source = await readSource(docsPagePath)
    const hrefs = [
      '/keys',
      '/dashboard/models',
      '/client-model-routes',
      '/install/',
    ]

    assert.match(source, /href=\{link\.href\}/)
    for (const href of hrefs) {
      assert.match(JSON.stringify(docsContent), new RegExp(href))
    }
    assert.match(source, /docsContent/)
    assert.match(source, /llmrouter\.quukk\.com/)
  })

  test('follows the site-wide i18n language instead of a custom locale', async () => {
    const source = await readSource(docsPagePath)

    assert.match(source, /useTranslation/)
    assert.match(source, /i18n\.language/)
    assert.match(source, /docsLanguageForInterface/)
    assert.doesNotMatch(source, /docs-locale/)
    assert.doesNotMatch(source, /showLanguageSwitcher:\s*false/)
    assert.doesNotMatch(source, /changeLanguage\(/)
    assert.doesNotMatch(source, /中文|English/)
  })

  test('has native copy controls with a selectable fallback and no API imports', async () => {
    const source = await readSource(docsPagePath)

    assert.match(source, /navigator\.clipboard\.writeText/)
    assert.match(source, /document\.execCommand\('copy'\)/)
    assert.match(source, /aria-label=\{`\$\{labels\.copy\}/)
    assert.match(source, /select-all/)
    assert.match(source, /https:\/\/llmrouter\.quukk\.com\/v1/)
    assert.match(source, /curl/)
    assert.doesNotMatch(source, /from ['"](?:@\/)?(?:api|hooks\/use-|lib\/api)/)
  })
})
