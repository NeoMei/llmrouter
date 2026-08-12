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
import { Check, Copy } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { PublicLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

import {
  docsContent,
  docsLanguageForInterface,
  type DocsLanguage,
} from './docs-content'

const baseUrl = 'https://llmrouter.quukk.com/v1'
const connectionCheck =
  'curl -sS https://llmrouter.quukk.com/v1/models -H "Authorization: Bearer $LLMROUTER_TOKEN"'

const pageLabels: Record<
  DocsLanguage,
  {
    copy: string
    copied: string
    baseUrl: string
    connectionCheck: string
    navigation: string
  }
> = {
  zhCN: {
    copy: '复制',
    copied: '已复制',
    baseUrl: 'Base URL',
    connectionCheck: '最小连接检查',
    navigation: '文档导航',
  },
  en: {
    copy: 'Copy',
    copied: 'Copied',
    baseUrl: 'Base URL',
    connectionCheck: 'Minimal connection check',
    navigation: 'Documentation navigation',
  },
  fr: {
    copy: 'Copier',
    copied: 'Copié',
    baseUrl: 'Base URL',
    connectionCheck: 'Vérification minimale',
    navigation: 'Navigation de la documentation',
  },
  ru: {
    copy: 'Копировать',
    copied: 'Скопировано',
    baseUrl: 'Base URL',
    connectionCheck: 'Минимальная проверка',
    navigation: 'Навигация по документации',
  },
  ja: {
    copy: 'コピー',
    copied: 'コピー済み',
    baseUrl: 'Base URL',
    connectionCheck: '最小接続確認',
    navigation: 'ドキュメントナビゲーション',
  },
  vi: {
    copy: 'Sao chép',
    copied: 'Đã sao chép',
    baseUrl: 'Base URL',
    connectionCheck: 'Kiểm tra tối thiểu',
    navigation: 'Điều hướng tài liệu',
  },
  zhTW: {
    copy: '複製',
    copied: '已複製',
    baseUrl: 'Base URL',
    connectionCheck: '最小連接檢查',
    navigation: '文件導覽',
  },
}

export function DocsPage() {
  const { i18n } = useTranslation()
  const [copiedValue, setCopiedValue] = useState<string | null>(null)
  const language = docsLanguageForInterface(i18n.language)
  const content = docsContent[language]
  const labels = pageLabels[language]

  useEffect(() => {
    if (!copiedValue) return

    const timeoutId = window.setTimeout(() => setCopiedValue(null), 1800)
    return () => window.clearTimeout(timeoutId)
  }, [copiedValue])

  async function copyText(value: string) {
    try {
      await navigator.clipboard.writeText(value)
    } catch {
      const textArea = document.createElement('textarea')
      textArea.value = value
      textArea.setAttribute('readonly', '')
      textArea.style.position = 'fixed'
      textArea.style.opacity = '0'
      document.body.append(textArea)
      textArea.select()
      const copied = document.execCommand('copy')
      textArea.remove()
      if (!copied) return
    }

    setCopiedValue(value)
  }

  return (
    <PublicLayout>
      <div className='mx-auto max-w-5xl pb-10'>
        <div className='max-w-2xl space-y-2 border-b pb-6'>
          <h1 className='text-3xl font-semibold tracking-tight'>
            {content.title}
          </h1>
          <p className='text-muted-foreground'>{content.introduction}</p>
        </div>

        <nav aria-label={labels.navigation} className='border-b lg:hidden'>
          <div className='flex gap-1 overflow-x-auto py-3'>
            {content.sections.map((section) => (
              <a
                key={section.id}
                href={`#${section.id}`}
                className='text-muted-foreground hover:text-foreground shrink-0 rounded-md px-2 py-1 text-sm transition-colors'
              >
                {section.title}
              </a>
            ))}
          </div>
        </nav>

        <div className='mt-6 grid gap-8 lg:grid-cols-[12rem_minmax(0,1fr)]'>
          <nav aria-label={labels.navigation} className='hidden lg:block'>
            <div className='sticky top-20 space-y-1'>
              {content.sections.map((section) => (
                <a
                  key={section.id}
                  href={`#${section.id}`}
                  className='text-muted-foreground hover:bg-muted hover:text-foreground block rounded-md px-3 py-2 text-sm transition-colors'
                >
                  {section.title}
                </a>
              ))}
            </div>
          </nav>

          <div className='space-y-5'>
            {content.sections.map((section) => (
              <section
                key={section.id}
                id={section.id}
                aria-labelledby={`${section.id}-title`}
                className='scroll-mt-24'
              >
                <Card>
                  <CardHeader>
                    <CardTitle>
                      <h2 id={`${section.id}-title`}>{section.title}</h2>
                    </CardTitle>
                  </CardHeader>
                  <CardContent className='space-y-4'>
                    <p className='text-muted-foreground leading-6'>
                      {section.body}
                    </p>

                    {section.id === 'connection-address' && (
                      <div className='bg-muted/40 rounded-lg border p-3'>
                        <div className='mb-2 flex items-center justify-between gap-3'>
                          <span className='text-sm font-medium'>
                            {labels.baseUrl}
                          </span>
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            aria-label={`${labels.copy} ${labels.baseUrl}`}
                            onClick={() => copyText(baseUrl)}
                          >
                            {copiedValue === baseUrl ? (
                              <Check data-icon='inline-start' />
                            ) : (
                              <Copy data-icon='inline-start' />
                            )}
                            {copiedValue === baseUrl
                              ? labels.copied
                              : labels.copy}
                          </Button>
                        </div>
                        <code className='bg-background block overflow-x-auto rounded px-3 py-2 text-sm select-all'>
                          {baseUrl}
                        </code>
                      </div>
                    )}

                    {section.agents && (
                      <ul className='space-y-3' aria-label={section.title}>
                        {section.agents.map((agent) => (
                          <li
                            key={agent.name}
                            className='rounded-md border p-3'
                          >
                            <p className='font-medium'>{agent.name}</p>
                            <p className='text-muted-foreground mt-1 text-sm leading-5'>
                              {agent.detail}
                            </p>
                          </li>
                        ))}
                      </ul>
                    )}

                    {section.id === 'verify' && (
                      <div className='bg-muted/40 rounded-lg border p-3'>
                        <div className='mb-2 flex items-center justify-between gap-3'>
                          <span className='text-sm font-medium'>
                            {labels.connectionCheck}
                          </span>
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            aria-label={`${labels.copy} ${labels.connectionCheck}`}
                            onClick={() => copyText(connectionCheck)}
                          >
                            {copiedValue === connectionCheck ? (
                              <Check data-icon='inline-start' />
                            ) : (
                              <Copy data-icon='inline-start' />
                            )}
                            {copiedValue === connectionCheck
                              ? labels.copied
                              : labels.copy}
                          </Button>
                        </div>
                        <code className='bg-background block overflow-x-auto rounded px-3 py-2 text-sm break-all select-all'>
                          {connectionCheck}
                        </code>
                      </div>
                    )}

                    {section.links.length > 0 && (
                      <div className='flex flex-wrap gap-2'>
                        {section.links.map((link) => (
                          <a
                            key={link.href}
                            href={link.href}
                            className='text-primary hover:bg-muted inline-flex rounded-md px-2 py-1 text-sm font-medium underline-offset-4 hover:underline'
                          >
                            {link.label}
                          </a>
                        ))}
                      </div>
                    )}
                  </CardContent>
                </Card>
              </section>
            ))}
          </div>
        </div>
      </div>
    </PublicLayout>
  )
}
