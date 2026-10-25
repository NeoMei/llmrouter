import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { message } from './session'
import { Panel, Field, Submit, type PanelProps } from './ui'

export function TeamPanel(
  props: PanelProps & { refresh: () => Promise<void> }
) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const canManage = ['owner', 'admin'].includes(props.account.role)
  const members = useQuery({
    queryKey: ['members', props.account.id],
    queryFn: () =>
      props.session.bound(() => props.session.client.members(props.account.id)),
    retry: false,
  })
  const invitations = useQuery({
    queryKey: ['invitations'],
    queryFn: () =>
      props.session.bound(() => props.session.client.invitations()),
    retry: false,
  })
  async function action(work: () => Promise<unknown>) {
    setBusy(true)
    setError('')
    try {
      await props.session.bound(work)
      await props.refresh()
      await client.invalidateQueries({ queryKey: ['members'] })
      await client.invalidateQueries({ queryKey: ['invitations'] })
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Panel title='Teams and members'>
      <form
        className='flex items-end gap-2'
        onSubmit={(e) => {
          e.preventDefault()
          const data = new FormData(e.currentTarget)
          void action(() =>
            props.session.client.createTeam(String(data.get('team')))
          )
        }}
      >
        <Field label='New team name' name='team' required />
        <Submit text='Create team' busy={busy} />
      </form>
      {invitations.error ? (
        <p role='alert'>{message(invitations.error)}</p>
      ) : null}
      {invitations.data?.map((invite) => (
        <div key={invite.id} className='flex items-center gap-2'>
          <span>
            {invite.account_id} · {invite.role}
          </span>
          <Button
            disabled={busy}
            onClick={() =>
              void action(() => props.session.client.inviteAccept(invite.id))
            }
          >
            {t('Accept invitation')}
          </Button>
        </div>
      ))}
      {members.error ? <p role='alert'>{message(members.error)}</p> : null}
      <ul className='space-y-2'>
        {members.data?.map((member) => (
          <li
            key={member.user_id}
            className='flex flex-wrap items-center gap-2'
          >
            <span>
              {member.name} · {member.user_id} · {member.role}
            </span>
            {canManage ? (
              <>
                <select
                  aria-label={`${t('Member role')} ${member.user_id}`}
                  value={member.role}
                  disabled={busy || member.role === 'owner'}
                  onChange={(e) =>
                    void action(() =>
                      props.session.client.changeMemberRole(
                        props.account.id,
                        member.user_id,
                        e.target.value
                      )
                    )
                  }
                >
                  {['owner', 'admin', 'billing_admin', 'member'].map((role) => (
                    <option key={role}>{role}</option>
                  ))}
                </select>
                <Button
                  variant='outline'
                  disabled={busy || member.role === 'owner'}
                  onClick={() =>
                    void action(() =>
                      props.session.client.removeMember(
                        props.account.id,
                        member.user_id
                      )
                    )
                  }
                >
                  {t('Remove member')}
                </Button>
              </>
            ) : null}
          </li>
        ))}
      </ul>
      {canManage ? (
        <form
          className='flex flex-wrap items-end gap-2'
          onSubmit={(e) => {
            e.preventDefault()
            const data = new FormData(e.currentTarget)
            void action(() =>
              props.session.client.invite(
                props.account.id,
                String(data.get('recipient')),
                String(data.get('role'))
              )
            )
          }}
        >
          <Field label='Recipient user ID' name='recipient' required />
          <select name='role' aria-label={t('Invitation role')}>
            {['member', 'admin', 'billing_admin'].map((role) => (
              <option key={role}>{role}</option>
            ))}
          </select>
          <Submit text='Invite member' busy={busy} />
        </form>
      ) : null}
      {error ? <p role='alert'>{error}</p> : null}
    </Panel>
  )
}
