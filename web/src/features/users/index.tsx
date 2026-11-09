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
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { useAuthStore } from '@/stores/auth-store'

import { CentralUsersTable } from './components/central-users-table'
import { UsersDeleteDialog } from './components/users-delete-dialog'
import { UsersMutateDrawer } from './components/users-mutate-drawer'
import { UsersPrimaryButtons } from './components/users-primary-buttons'
import { UsersProvider, useUsers } from './components/users-provider'
import { UsersTable } from './components/users-table'
import { isCentralUserDirectory } from './lib/central-user-view'

function UsersContent() {
  const { t } = useTranslation()
  const { open, setOpen, currentRow } = useUsers()
  const centralIdentity = useAuthStore((state) =>
    isCentralUserDirectory(state.auth.user, state.auth.session)
  )

  return (
    <>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>{t('Users')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          {centralIdentity ? null : <UsersPrimaryButtons />}
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          {centralIdentity ? <CentralUsersTable /> : <UsersTable />}
        </SectionPageLayout.Content>
      </SectionPageLayout>

      {centralIdentity ? null : (
        <>
          <UsersMutateDrawer
            open={open === 'create' || open === 'update'}
            onOpenChange={(isOpen) => !isOpen && setOpen(null)}
            currentRow={open === 'update' ? currentRow || undefined : undefined}
          />
          <UsersDeleteDialog />
        </>
      )}
    </>
  )
}

export function Users() {
  return (
    <UsersProvider>
      <UsersContent />
    </UsersProvider>
  )
}
