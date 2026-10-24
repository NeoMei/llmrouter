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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { hasPermission } from '@/lib/admin-permissions'
import { useAuthStore } from '@/stores/auth-store'

import {
  mutateAccountPool,
  type PoolAction,
  type PoolMember,
  type PoolMutation,
  type PoolSnapshot,
  type RecoveryScope,
} from '../../account-pool'

export function PoolMemberCard(props: {
  snapshot: PoolSnapshot
  member: PoolMember
  busy: boolean
}) {
  const { t } = useTranslation()
  const user = useAuthStore((s) => s.auth.user)
  const canOperate = hasPermission(user, 'channel', 'operate')
  const canWrite = hasPermission(user, 'channel', 'write')
  const [group, setGroup] = useState(props.member.quota_group)
  const [scope, setScope] = useState<RecoveryScope>('selected')
  const [confirm, setConfirm] = useState<PoolAction | null>(null)
  const client = useQueryClient()
  const mutation = useMutation({
    mutationFn: (action: PoolAction) => {
      const body: PoolMutation = {
        ...props.snapshot.selected_scope,
        profile_id: props.snapshot.profile_id || 0,
        pool_id: props.snapshot.pool_id,
        member_id: props.member.member_id,
        target_scope: 'account_global',
      }
      if (action === 'recover') {
        body.generation_id = props.member.generation_id
        body.target_scope = scope
      }
      if (action === 'recover-group') {
        body.quota_group = props.member.quota_group
        body.target_scope = scope
      }
      if (action === 'quota-group') {
        body.quota_group = group
        body.expected_quota_group = props.member.quota_group
      }
      return mutateAccountPool(props.snapshot.channel_id, action, body)
    },
    onError: () => setConfirm(null),
    onSuccess: async () => {
      setConfirm(null)
      await client.invalidateQueries({
        queryKey: ['account-pool', props.snapshot.channel_id],
      })
    },
  })
  const busy = props.busy || mutation.isPending
  const member = props.member
  return (
    <article
      className='space-y-2 rounded border p-3'
      aria-label={`${t('Account')} ${member.indices.map((i) => i + 1).join(', ')}`}
    >
      <div className='flex flex-wrap gap-2'>
        <strong>
          {t('Account')} {member.indices.map((i) => i + 1).join(', ')}
        </strong>
        <Badge variant='outline'>{member.state}</Badge>
      </div>
      <p className='font-mono text-xs break-all'>
        {t('Stable account')}: {member.member_id}
        <br />
        {t('Current generation')}: {member.generation_id || '—'}
      </p>
      {!!member.legacy_disabled_indices?.length && (
        <p>
          {t('Legacy disabled positions')}:{' '}
          {member.legacy_disabled_indices.map((i) => i + 1).join(', ')}
        </p>
      )}
      {member.retry_at && (
        <p>
          {t('Blocking expiry')}: {member.retry_at}
        </p>
      )}
      <ul className='text-xs'>
        {member.blockers.map((blocker) => (
          <li
            key={`${blocker.reason}/${blocker.scope}/${blocker.operation}/${blocker.model}/${blocker.retry_at}`}
          >
            {blocker.reason} · {blocker.scope} · {blocker.operation || '*'} ·{' '}
            {blocker.model || '*'} {blocker.retry_at || ''}
          </li>
        ))}
      </ul>
      {mutation.isError && (
        <p role='alert'>
          {t('Account action failed. Refresh before retrying.')} (
          {mutation.error.message})
        </p>
      )}
      {mutation.isPending && <p role='status'>{t('Saving...')}</p>}
      <fieldset disabled={busy || mutation.isError} className='space-y-2'>
        <div className='flex flex-wrap gap-2'>
          <Button
            size='sm'
            disabled={!canOperate}
            onClick={() => setConfirm(member.paused ? 'resume' : 'pause')}
          >
            {member.paused
              ? t('Resume manual pause')
              : t('Pause new admissions')}
          </Button>
          <label className='text-sm'>
            {t('Recovery scope')}
            <select
              className='ml-2 rounded border p-1'
              value={scope}
              onChange={(event) =>
                setScope(event.target.value as RecoveryScope)
              }
            >
              <option value='selected'>
                {t('Selected operation and model')}
              </option>
              <option value='operation'>{t('Operation across models')}</option>
              <option value='model'>{t('Model across operations')}</option>
              <option value='account_global'>
                {t('Account global health')}
              </option>
            </select>
          </label>
          <Button
            size='sm'
            disabled={!canOperate || !member.generation_id}
            onClick={() => setConfirm('recover')}
          >
            {t('Recover current generation')}
          </Button>
          <Button
            size='sm'
            variant='outline'
            disabled={!canOperate || !member.quota_group}
            onClick={() => setConfirm('recover-group')}
          >
            {t('Recover shared quota group')}
          </Button>
        </div>
        <label className='block text-sm'>
          {t('Global quota group')}
          <Input
            value={group}
            disabled={!canWrite}
            maxLength={96}
            onChange={(event) => setGroup(event.target.value)}
          />
        </label>
        <Button
          size='sm'
          variant='outline'
          disabled={
            !canWrite ||
            group === member.quota_group ||
            !/^[A-Za-z0-9:_.-]{0,96}$/.test(group)
          }
          onClick={() => setConfirm('quota-group')}
        >
          {t('Assign quota group')}
        </Button>
      </fieldset>
      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(open) =>
          !open && !mutation.isPending && setConfirm(null)
        }
        title={t('Confirm account action')}
        desc={
          confirm === 'recover-group' || confirm === 'quota-group'
            ? t(
                'Quota groups are global across channels and pools. This action uses the current stored membership and affects the explicitly selected scope.'
              )
            : t(
                'Only the named state changes. Recovery preserves manual pause and unrelated scopes. Refresh confirms the resulting availability.'
              )
        }
        confirmText={t('Apply')}
        isLoading={mutation.isPending}
        disabled={busy || mutation.isError}
        handleConfirm={() => confirm && mutation.mutate(confirm)}
      >
        <p className='font-mono text-xs break-all'>
          {confirm} · {member.member_id}
          <br />
          {confirm === 'recover' || confirm === 'recover-group'
            ? scope
            : 'account_global'}{' '}
          · {props.snapshot.selected_scope.operation} ·{' '}
          {props.snapshot.selected_scope.model}
          <br />
          {confirm === 'quota-group'
            ? `${member.quota_group || '—'} → ${group || '—'}`
            : member.quota_group}
        </p>
      </ConfirmDialog>
    </article>
  )
}
