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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Empty, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { hasPermission } from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  getPoolOperations,
  getAccountPool,
  type PoolSnapshot,
} from '../../account-pool'
import type { Channel } from '../../types'
import { PoolMemberCard } from './account-pool-member'

export function AccountPoolDialog(props: {
  channel: Pick<Channel, 'id' | 'name' | 'type' | 'models' | 'model_mapping'>
  onClose: () => void
}) {
  const { t } = useTranslation()
  const user = useAuthStore((s) => s.auth.user)
  const canRead = hasPermission(user, 'channel', 'read')
  const capabilities = useQuery({
    queryKey: ['account-pool-operations', props.channel.type],
    queryFn: () => getPoolOperations(props.channel.type),
    enabled: canRead,
    retry: false,
  })
  const models = props.channel.models
    .split(',')
    .map((value) => value.trim())
    .filter(Boolean)
  let mapping: Record<string, string> = {}
  try {
    const parsed: unknown = JSON.parse(props.channel.model_mapping || '{}')
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      mapping = Object.fromEntries(
        Object.entries(parsed).filter(
          (entry): entry is [string, string] => typeof entry[1] === 'string'
        )
      )
    }
  } catch {
    /* server validates configured models */
  }
  const upstreamModels = [
    ...new Set(models.map((model) => mapping[model] || model)),
  ]
  const [model, setModel] = useState(upstreamModels[0] || '')
  const [operation, setOperation] = useState(
    props.channel.type === 60 ? 'ocr.jobs.create' : 'chat.completions.create'
  )
  const [selected, setSelected] = useState({ operation, model })
  const query = useQuery({
    queryKey: ['account-pool', props.channel.id, selected],
    queryFn: () => getAccountPool(props.channel.id, selected),
    enabled: canRead && !!selected.model,
    retry: false,
    staleTime: 0,
  })
  return (
    <Dialog open onOpenChange={(open) => !open && props.onClose()}>
      <DialogContent className='max-h-[90vh] overflow-y-auto sm:max-w-4xl'>
        <DialogHeader>
          <DialogTitle>
            {t('Account pool')} · {props.channel.name}
          </DialogTitle>
          <DialogDescription>
            {t('Scoped server snapshot. Remaining supplier quota is unknown.')}
          </DialogDescription>
        </DialogHeader>
        {!canRead ? (
          <p role='alert'>{t('No permission to perform this action')}</p>
        ) : (
          <>
            <p className='text-muted-foreground text-sm'>
              {t(
                'Pause blocks new admissions; already admitted requests may finish. Resume clears only manual pause.'
              )}
            </p>
            {props.channel.type === 59 && (
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Gateway profiles and models require trusted server provisioning. Account actions affect the stable owner; health recovery affects the current credential generation.'
                )}
              </p>
            )}
            <div className='grid gap-3 sm:grid-cols-2'>
              <label>
                {t('Upstream model')}
                <select
                  className='mt-1 w-full rounded border p-2'
                  value={model}
                  onChange={(event) => setModel(event.target.value)}
                >
                  {upstreamModels.map((value) => (
                    <option key={value}>{value}</option>
                  ))}
                </select>
              </label>
              <label>
                {t('Diagnostic operation')}
                <Input
                  list='pool-operations'
                  value={operation}
                  onChange={(event) => setOperation(event.target.value)}
                />
                <datalist id='pool-operations'>
                  {(capabilities.data || []).map((value) => (
                    <option key={value} value={value} />
                  ))}
                </datalist>
              </label>
            </div>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Diagnostic scopes do not certify model support or purchased capability. Use the configured upstream operation.'
              )}
            </p>
            <Button
              disabled={!model || !operation || query.isFetching}
              onClick={() => {
                if (
                  selected.model === model &&
                  selected.operation === operation
                ) {
                  void query.refetch()
                } else setSelected({ model, operation })
              }}
            >
              {t('Refresh scope')}
            </Button>
            {query.isFetching && (
              <div role='status'>
                <span>{t('Loading...')}</span>
                <Skeleton className='mt-2 h-8 w-full' />
              </div>
            )}
            {query.isError && (
              <Alert variant='destructive'>
                <AlertDescription>
                  {t(
                    'Account pool could not be refreshed. Actions are disabled.'
                  )}{' '}
                  ({query.error.message})
                </AlertDescription>
              </Alert>
            )}
            {query.data && !query.isError && (
              <PoolView
                key={`${query.data.pool_id}/${selected.operation}/${selected.model}/${query.dataUpdatedAt}`}
                snapshot={query.data}
                busy={query.isFetching}
              />
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

function PoolView(props: { snapshot: PoolSnapshot; busy: boolean }) {
  const { t } = useTranslation()
  const snapshot = props.snapshot
  const countLabels = {
    total: t('Total'),
    available: t('Available'),
    cooling: t('Cooling'),
    isolated: t('Isolated'),
    paused: t('Paused'),
    legacy_blocked: t('Legacy blocked'),
    unavailable: t('Unavailable'),
  }
  return (
    <section className='space-y-3' aria-label={t('Pool snapshot')}>
      <p className='text-sm'>
        {snapshot.selected_scope.operation} · {snapshot.selected_scope.model}
      </p>
      <p className='text-muted-foreground text-xs'>
        {t('Server time')}: {snapshot.server_time} · {snapshot.status}
      </p>
      <div className='flex flex-wrap gap-2'>
        {Object.entries(countLabels).map(([key, label]) => (
          <Badge variant='secondary' key={key}>
            {label}: {snapshot.counts[key as keyof typeof countLabels]}
          </Badge>
        ))}
      </div>
      {snapshot.retry_at && (
        <p>
          {t('Earliest timed recovery')}: {snapshot.retry_at}
        </p>
      )}
      <p className='text-muted-foreground text-xs'>
        {t(
          'Expiry is server reported, not a guarantee of capacity. Legacy configuration barriers require their existing configuration controls.'
        )}
      </p>
      {!snapshot.members.length && (
        <Empty>
          <EmptyHeader>
            <EmptyTitle>{t('No accounts in this pool')}</EmptyTitle>
          </EmptyHeader>
        </Empty>
      )}
      {snapshot.members.map((member) => (
        <PoolMemberCard
          key={member.member_id}
          snapshot={snapshot}
          member={member}
          busy={props.busy}
        />
      ))}
    </section>
  )
}
