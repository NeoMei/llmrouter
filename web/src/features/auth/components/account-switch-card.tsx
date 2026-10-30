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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { switchAuthenticationSession } from '@/lib/auth-session'
import { useAuthStore } from '@/stores/auth-store'

import { fetchSessionOptions } from '../session-options'

export function AccountSwitchCard() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const currentSID = useAuthStore((state) => state.auth.session?.sid)
  const [switching, setSwitching] = useState(false)
  const [switchFailed, setSwitchFailed] = useState(false)
  const options = useQuery({
    queryKey: ['auth', 'session-options'],
    queryFn: fetchSessionOptions,
    retry: false,
    staleTime: 0,
  })
  const sessions = options.data?.sessions ?? []
  const choices = sessions.filter((session) => session.sid !== currentSID)

  async function choose(sid: string) {
    setSwitchFailed(false)
    setSwitching(true)
    await queryClient.cancelQueries()
    const outcome = await switchAuthenticationSession(
      sid,
      sessions.map((session) => session.sid)
    )
    if (outcome.kind === 'authenticated') {
      queryClient.clear()
      window.location.assign('/dashboard')
      return
    }
    setSwitching(false)
    setSwitchFailed(true)
    void queryClient.invalidateQueries()
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('Switch account')}</CardTitle>
      </CardHeader>
      <CardContent className='space-y-3'>
        {choices.map((session) => (
          <div
            key={session.sid}
            className='flex flex-wrap items-center justify-between gap-3'
          >
            <div className='min-w-0'>
              <p className='truncate font-medium'>
                {session.display_name ||
                  t(
                    session.realm !== 'central'
                      ? 'Local account'
                      : 'Central account'
                  )}
              </p>
              <p className='text-muted-foreground text-sm'>
                {t(
                  session.realm !== 'central'
                    ? 'Local account'
                    : 'Central account'
                )}
              </p>
            </div>
            <Button
              type='button'
              variant='outline'
              disabled={switching}
              onClick={() => void choose(session.sid)}
            >
              {t('Switch account')}
            </Button>
          </div>
        ))}
        {!options.isLoading &&
          choices.length === 0 &&
          !options.isError &&
          !options.data?.unavailable && (
            <p className='text-muted-foreground text-sm'>
              {t('No other signed-in account')}
            </p>
          )}
        {(options.isError || options.data?.unavailable) && (
          <div role='alert' className='space-y-2 text-sm'>
            <p>{t('Account choices temporarily unavailable')}</p>
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => void options.refetch()}
            >
              {t('Try again')}
            </Button>
          </div>
        )}
        {switchFailed && (
          <p role='alert' className='text-destructive text-sm'>
            {t('Unable to switch account')}
          </p>
        )}
      </CardContent>
    </Card>
  )
}
