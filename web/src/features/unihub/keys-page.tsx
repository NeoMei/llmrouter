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
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'

import { ProductShell } from './index'
import { message, type ProductConfig } from './session'

export function UnifiedKeys() {
  const { t } = useTranslation()
  const config = useQuery({
    queryKey: ['unihub-public-config'],
    queryFn: async () => {
      const response = await fetch('/api/unihub/config', {
        credentials: 'omit',
        cache: 'no-store',
      })
      if (!response.ok) throw new Error('UniHub configuration unavailable')
      return response.json() as Promise<ProductConfig>
    },
    retry: false,
  })
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('API Keys')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        {config.isPending ? <p>{t('Loading')}</p> : null}
        {config.error ? <p role='alert'>{message(config.error)}</p> : null}
        {config.data?.enabled ? (
          <ProductShell config={config.data} page='keys' />
        ) : null}
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
