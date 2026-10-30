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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { ManagedAccess, limitAmount, type UsageBudget } from './managed-access'
import type { FundingMode } from './sdk'
import { exactAmount, message } from './session'
import { Panel, Field, Submit, type PanelProps } from './ui'

function editableAmount(value: string): string {
  return value === '' ? '' : exactAmount(value, 'CREDIT/6').split(' ')[0]
}

function BudgetForm(props: {
  budget: UsageBudget
  busy: boolean
  save: (total: string, perCall: string) => void
}) {
  const { t } = useTranslation()
  const [total, setTotal] = useState(() =>
    editableAmount(props.budget.max_total)
  )
  const [perCall, setPerCall] = useState(() =>
    editableAmount(props.budget.max_per_call)
  )
  return (
    <form
      className='grid gap-3 sm:grid-cols-2'
      onSubmit={(event) => {
        event.preventDefault()
        const form = new FormData(event.currentTarget)
        props.save(
          String(form.get('budget_total') ?? ''),
          String(form.get('budget_per_call') ?? '')
        )
      }}
    >
      <Field
        name='budget_total'
        label='Account lifetime budget'
        value={total}
        onChange={setTotal}
      />
      <Field
        name='budget_per_call'
        label='Account per-call limit'
        value={perCall}
        onChange={setPerCall}
      />
      <p className='text-muted-foreground text-sm sm:col-span-2'>
        {t('Current limits')}:{' '}
        {props.budget.max_total === ''
          ? t('Unlimited')
          : exactAmount(props.budget.max_total, props.budget.asset)}{' '}
        ·{' '}
        {props.budget.max_per_call === ''
          ? t('Unlimited per call')
          : exactAmount(props.budget.max_per_call, props.budget.asset)}
      </p>
      <Submit text='Save account budget' busy={props.busy} />
    </form>
  )
}

export function ApiKeysPanel(props: PanelProps) {
  return (
    <ScopedApiKeysPanel
      key={`${props.session.epoch}:${props.account.id}:${props.config.product_id}:${props.config.instance_id}`}
      {...props}
    />
  )
}

function ScopedApiKeysPanel(props: PanelProps) {
  const { t } = useTranslation()
  const cache = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [secret, setSecret] = useState('')
  const [modeOverride, setModeOverride] = useState<FundingMode>()
  const [models, setModels] = useState<string[]>([])
  const access = new ManagedAccess(
    props.session.client,
    props.account.id,
    props.config.product_id,
    props.config.instance_id ?? 'default'
  )
  const finance = ['owner', 'billing_admin'].includes(props.account.role)
  const profile = useQuery({
    queryKey: ['billing-profile', props.config.product_id],
    queryFn: () =>
      props.session.bound(() =>
        props.session.client.billingProfile(props.config.product_id)
      ),
    retry: false,
  })
  const profileReady = profile.isSuccess && !profile.isFetching
  const mode = profileReady
    ? (modeOverride ?? profile.data?.usage.default_funding_mode ?? '')
    : ''
  const asset = mode === 'prepaid_cash' ? 'CNY/6' : 'CREDIT/6'
  const keys = useQuery({
    queryKey: [
      'managed-keys',
      props.session.epoch,
      props.account.id,
      props.config.product_id,
      props.config.instance_id,
    ],
    queryFn: () => props.session.bound(() => access.keys()),
    retry: false,
  })
  const budget = useQuery({
    queryKey: ['usage-budget', props.account.id, mode],
    queryFn: () =>
      props.session.bound(() => access.budget(mode as FundingMode)),
    enabled: !!mode && finance,
    retry: false,
  })
  const resources = useQuery({
    queryKey: ['resources', props.account.id],
    queryFn: () =>
      props.session.bound(() =>
        props.session.client.resources(props.account.id)
      ),
    retry: false,
  })
  const keysReady = keys.isSuccess && !keys.isFetching
  const resourcesReady = resources.isSuccess && !resources.isFetching
  const budgetReady = !!mode && budget.isSuccess && !budget.isFetching
  const currentModeAllowed =
    !!mode && !!profile.data?.usage.funding_modes.includes(mode as FundingMode)
  const canCreate =
    props.session.canFinanceWrite(
      'model-credentials:write',
      props.account.id
    ) &&
    profileReady &&
    resourcesReady &&
    keysReady &&
    (!finance || budgetReady) &&
    currentModeAllowed &&
    !!profile.data?.usage.enabled
  useEffect(() => {
    if (
      profile.isError ||
      resources.isError ||
      keys.isError ||
      budget.isError
    ) {
      setSecret('')
    }
  }, [profile.isError, resources.isError, keys.isError, budget.isError])
  const availableFunds =
    resourcesReady && mode === 'credits'
      ? resources.data?.credits?.spendable
      : resourcesReady && mode === 'prepaid_cash'
        ? resources.data?.wallets?.find(
            (wallet) =>
              wallet.product_id === props.config.product_id &&
              wallet.asset === asset
          )?.amount
        : undefined

  async function create(form: HTMLFormElement) {
    if (busy || !mode || !canCreate || models.length === 0) return
    setBusy(true)
    setError('')
    setSecret('')
    const values = new FormData(form)
    try {
      const expires = String(values.get('expires') ?? '')
      const key = await props.session.bound(() =>
        access.createKey({
          name: String(values.get('name') ?? ''),
          models,
          funding_mode: mode as FundingMode,
          max_total: limitAmount(String(values.get('key_total') ?? '')),
          max_per_call: limitAmount(String(values.get('key_per_call') ?? '')),
          ...(expires ? { expires_at: new Date(expires).toISOString() } : {}),
        })
      )
      setSecret(key.token)
      form.reset()
      await cache.invalidateQueries({
        queryKey: [
          'managed-keys',
          props.session.epoch,
          props.account.id,
          props.config.product_id,
          props.config.instance_id,
        ],
      })
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  async function revoke(id: string) {
    if (
      busy ||
      !keysReady ||
      !props.session.canFinanceWrite(
        'model-credentials:write',
        props.account.id
      )
    ) {
      return
    }
    setBusy(true)
    setError('')
    setSecret('')
    try {
      await props.session.bound(() => access.revokeKey(id))
      await cache.invalidateQueries({
        queryKey: [
          'managed-keys',
          props.session.epoch,
          props.account.id,
          props.config.product_id,
          props.config.instance_id,
        ],
      })
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  async function saveBudget(total: string, perCall: string) {
    if (
      busy ||
      !props.session.canFinanceWrite('settings:write', props.account.id) ||
      !budgetReady ||
      !profileReady ||
      !resourcesReady ||
      !keysReady ||
      !currentModeAllowed ||
      !budget.data ||
      !mode
    ) {
      return
    }
    const version = budget.data.version
    setBusy(true)
    setError('')
    try {
      await props.session.bound(() =>
        access.setBudget(mode as FundingMode, {
          version,
          max_total: limitAmount(total),
          max_per_call: limitAmount(perCall),
        })
      )
      await cache.invalidateQueries({
        queryKey: ['usage-budget', props.account.id, mode],
      })
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <Panel title='API Keys'>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Keys belong to the selected payer and remain valid until revoked or expired. Use them with this LLMRouter API endpoint.'
          )}
        </p>
        <code className='block break-all'>{window.location.origin}/v1</code>
        <p className='text-muted-foreground text-sm'>
          {t(
            'This form preserves primary model permissions. Configure SuperWagie skills and knowledge permissions in UniHub.'
          )}{' '}
          <a
            className='underline'
            href={`${props.config.public_origin}/?view=api-keys&account=${encodeURIComponent(props.account.id)}`}
          >
            {t('Configure SuperWagie API key permissions')}
          </a>
        </p>
        <label className='grid gap-1 text-sm'>
          {t('Funding mode')}
          <select
            className='bg-background rounded border p-2'
            aria-label={t('Funding mode')}
            value={mode}
            disabled={!profileReady || busy}
            onChange={(event) =>
              setModeOverride(event.target.value as FundingMode)
            }
          >
            {!mode ? (
              <option value=''>{t('Choose funding mode')}</option>
            ) : null}
            {(profileReady ? profile.data?.usage.funding_modes : [])?.map(
              (item) => (
                <option value={item} key={item}>
                  {t(item === 'credits' ? 'Model credits' : 'Prepaid cash')}
                </option>
              )
            )}
          </select>
        </label>
        {profile.error ? <p role='alert'>{message(profile.error)}</p> : null}
        {resources.error ? (
          <p role='alert'>{message(resources.error)}</p>
        ) : null}
        <p>
          {t('Available funds')}:{' '}
          {resources.isPending || profile.isPending ? (
            t('Loading')
          ) : availableFunds !== undefined ? (
            exactAmount(availableFunds, asset)
          ) : (
            <>
              <span>
                {t(
                  'Account balance is unavailable with your current permissions.'
                )}
              </span>{' '}
              <a href='/unihub' className='underline'>
                {t('View account charges in UniHub')}
              </a>
            </>
          )}
        </p>
        <form
          className='grid gap-3 sm:grid-cols-2'
          onSubmit={(event) => {
            event.preventDefault()
            void create(event.currentTarget)
          }}
        >
          <Field name='name' label='Key name' required />
          <Field
            name='expires'
            label='Expires at (optional)'
            type='datetime-local'
          />
          <Field name='key_total' label='Key lifetime limit' />
          <Field name='key_per_call' label='Key per-call limit' />
          <p className='text-muted-foreground text-sm sm:col-span-2'>
            {t(
              'Limits use the selected asset. Leave empty for unlimited; zero blocks new calls. A budget does not add funds.'
            )}{' '}
            · {asset}
          </p>
          <fieldset className='grid max-h-56 gap-2 overflow-auto rounded border p-3 sm:col-span-2'>
            <legend>{t('Allowed models')}</legend>
            {props.config.models.map((model) => (
              <label key={model} className='flex items-center gap-2 text-sm'>
                <input
                  type='checkbox'
                  value={model}
                  checked={models.includes(model)}
                  disabled={busy}
                  onChange={(event) =>
                    setModels((previous) =>
                      event.target.checked
                        ? [...previous, model]
                        : previous.filter((item) => item !== model)
                    )
                  }
                />
                {model}
              </label>
            ))}
            {!props.config.models.length ? (
              <p>{t('No published models are available.')}</p>
            ) : null}
          </fieldset>
          <Submit
            text='Create API key'
            busy={busy || !mode || models.length === 0 || !canCreate}
          />
        </form>
        {secret &&
        !profile.isError &&
        !resources.isError &&
        !keys.isError &&
        !budget.isError ? (
          <div className='space-y-2 rounded border p-3' role='status'>
            <p>{t('Copy this key now. It will only be shown once.')}</p>
            <code className='block break-all select-all'>{secret}</code>
            <Button variant='outline' onClick={() => setSecret('')}>
              {t('Hide key')}
            </Button>
          </div>
        ) : null}
        {keys.isPending ? <p>{t('Loading')}</p> : null}
        {keys.error ? <p role='alert'>{message(keys.error)}</p> : null}
        <div className='overflow-auto'>
          <table className='w-full text-left text-sm'>
            <thead>
              <tr>
                {[
                  'Name',
                  'Status',
                  'Allowed models',
                  'Used',
                  'Reserved',
                  'Limit',
                  'Expires at',
                  'Actions',
                ].map((label) => (
                  <th className='p-2' key={label}>
                    {t(label)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(keysReady ? keys.data : [])
                ?.filter(
                  (key) =>
                    (finance || key.actor_id === props.session.user?.id) &&
                    key.account_id === props.account.id &&
                    key.origin_product_id === props.config.product_id &&
                    key.instance_id === (props.config.instance_id ?? 'default')
                )
                .map((key) => (
                  <tr key={key.id} className='border-t'>
                    <td className='p-2'>{key.name}</td>
                    <td>
                      {t(
                        key.revoked_at
                          ? 'Revoked'
                          : key.expires_at &&
                              Date.parse(key.expires_at) <= Date.now()
                            ? 'Expired'
                            : 'Active'
                      )}
                    </td>
                    <td className='max-w-56 p-2 break-words'>
                      {key.models.join(', ')}
                      <details>
                        <summary>
                          {t('Reported capability restrictions')}
                        </summary>
                        {key.operations ? (
                          Object.entries(key.operations).map(
                            ([model, operations]) => (
                              <p key={model}>
                                {model}: {operations.join(', ') || '—'}
                              </p>
                            )
                          )
                        ) : (
                          <p>
                            {t(
                              'Capabilities are checked against current published rates when the key is used.'
                            )}
                          </p>
                        )}
                      </details>
                    </td>
                    <td>{exactAmount(key.used, key.asset)}</td>
                    <td>{exactAmount(key.held, key.asset)}</td>
                    <td>
                      {key.max_total === ''
                        ? t('Unlimited')
                        : exactAmount(key.max_total, key.asset)}
                    </td>
                    <td>
                      {key.expires_at
                        ? new Date(key.expires_at).toLocaleString()
                        : t('Never')}
                    </td>
                    <td>
                      {!key.revoked_at &&
                      props.session.canFinanceWrite(
                        'model-credentials:write',
                        props.account.id
                      ) ? (
                        <Button
                          variant='outline'
                          disabled={busy}
                          onClick={() => void revoke(key.id)}
                        >
                          {t('Revoke')}
                        </Button>
                      ) : null}
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
        {keysReady && keys.data?.length === 0 ? (
          <p>{t('No API keys yet.')}</p>
        ) : null}
      </Panel>
      <Panel title='Usage budget'>
        <p>
          {t(
            'The account budget applies to all keys for this product and instance. Wallet balance and consumption limits are separate.'
          )}
        </p>
        {budgetReady &&
        profileReady &&
        resourcesReady &&
        keysReady &&
        currentModeAllowed &&
        budget.data ? (
          <>
            <p>
              {t('Used')}: {exactAmount(budget.data.used, budget.data.asset)} ·{' '}
              {t('Reserved')}:{' '}
              {exactAmount(budget.data.held, budget.data.asset)}
            </p>
            {props.session.canFinanceWrite(
              'settings:write',
              props.account.id
            ) ? (
              <BudgetForm
                key={`${mode}:${budget.data.version}`}
                budget={budget.data}
                busy={busy}
                save={(total, perCall) => void saveBudget(total, perCall)}
              />
            ) : null}
          </>
        ) : null}
        {budget.error ? <p role='alert'>{message(budget.error)}</p> : null}
        {!finance ? (
          <p>
            {t(
              'An account owner or billing administrator manages the shared budget. You can set limits on your own keys.'
            )}
          </p>
        ) : null}
      </Panel>
      {error ? <p role='alert'>{error}</p> : null}
    </>
  )
}
