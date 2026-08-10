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
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'

import {
  createClientModelRoute,
  deleteClientModelRoute,
  getClientModelRouteErrorMessage,
  getClientModelRouteOptions,
  getClientModelRoutes,
  updateClientModelRoute,
  type ClientModelRoute,
  type ClientModelRouteChannelOption,
  type ClientModelRouteInput,
  type ClientModelTarget,
} from './api'
import {
  parsePreferredWires,
  serializePreferredWires,
  validateAliases,
  WIRE_PROTOCOLS,
  type WireProtocol,
} from './validation'

const emptyTarget = (): ClientModelTarget => ({
  channel_id: 0,
  upstream_model: '',
  billing_model: '',
  preferred_wires: '[]',
  priority: 0,
  weight: 1,
  enabled: true,
})

const emptyRoute = (): ClientModelRouteInput => ({
  public_id: '',
  display_name: '',
  client: 'all',
  aliases: [],
  metadata: '',
  enabled: true,
  targets: [emptyTarget()],
})

export function ClientModelRoutesPage() {
  const { t } = useTranslation()
  const [routes, setRoutes] = useState<ClientModelRoute[]>([])
  const [channels, setChannels] = useState<ClientModelRouteChannelOption[]>([])
  const [billingModels, setBillingModels] = useState<string[]>([])
  const [form, setForm] = useState<ClientModelRouteInput>(emptyRoute)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [deletingRoute, setDeletingRoute] = useState<ClientModelRoute | null>(
    null
  )
  const [deleting, setDeleting] = useState(false)

  const channelsByID = useMemo(
    () => new Map(channels.map((channel) => [channel.id, channel])),
    [channels]
  )

  const reload = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [routeResponse, optionsResponse] = await Promise.all([
        getClientModelRoutes(),
        getClientModelRouteOptions(),
      ])
      if (!routeResponse.success) {
        throw new Error(routeResponse.message || t('Failed to load routes'))
      }
      if (!optionsResponse.success || !optionsResponse.data) {
        throw new Error(
          optionsResponse.message || t('Failed to load route options')
        )
      }
      setRoutes(routeResponse.data || [])
      setChannels(optionsResponse.data.channels || [])
      setBillingModels(optionsResponse.data.billing_models || [])
    } catch (cause) {
      setError(
        getClientModelRouteErrorMessage(cause, t('Failed to load routes'))
      )
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    void reload()
  }, [reload])

  const submit = async () => {
    setError('')
    const aliases = form.aliases.map((alias) => alias.trim()).filter(Boolean)
    if (!validateAliases(aliases)) {
      setError(t('Aliases must be unique'))
      return
    }
    if (!form.public_id.trim() || form.targets.length === 0) {
      setError(t('Public ID and at least one target are required'))
      return
    }
    const invalidTarget = form.targets.find(
      (target) =>
        target.channel_id <= 0 ||
        !target.upstream_model.trim() ||
        !target.billing_model.trim() ||
        parsePreferredWires(target.preferred_wires).length === 0 ||
        target.weight < 1
    )
    if (invalidTarget) {
      setError(t('Complete every target and select at least one protocol'))
      return
    }
    if (form.enabled && !form.targets.some((target) => target.enabled)) {
      setError(t('An enabled route needs at least one enabled target'))
      return
    }

    const input = { ...form, public_id: form.public_id.trim(), aliases }
    setSaving(true)
    try {
      const response = form.id
        ? await updateClientModelRoute(
            input as ClientModelRouteInput & { id: number }
          )
        : await createClientModelRoute(input)
      if (!response.success) {
        throw new Error(response.message || t('Failed to save route'))
      }
      setForm(emptyRoute())
      await reload()
    } catch (cause) {
      setError(
        getClientModelRouteErrorMessage(cause, t('Failed to save route'))
      )
    } finally {
      setSaving(false)
    }
  }

  const edit = (route: ClientModelRoute) => {
    setError('')
    setForm({
      id: route.id,
      public_id: route.public_id,
      display_name: route.display_name,
      client: route.client,
      aliases: [...route.aliases],
      metadata: route.metadata || '',
      enabled: route.enabled,
      targets: route.targets.map((target) => ({ ...target })),
    })
  }

  const remove = async () => {
    if (!deletingRoute) return
    setDeleting(true)
    setError('')
    try {
      const response = await deleteClientModelRoute(deletingRoute.id)
      if (!response.success) {
        throw new Error(response.message || t('Failed to delete route'))
      }
      if (form.id === deletingRoute.id) setForm(emptyRoute())
      setDeletingRoute(null)
      await reload()
    } catch (cause) {
      setError(
        getClientModelRouteErrorMessage(cause, t('Failed to delete route'))
      )
    } finally {
      setDeleting(false)
    }
  }

  return (
    <>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>
          {t('Client Model Routes')}
        </SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <Button
            onClick={() => {
              setError('')
              setForm(emptyRoute())
            }}
            variant='outline'
          >
            {t('New route')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='grid min-h-0 gap-4 overflow-auto lg:grid-cols-[minmax(0,1fr)_minmax(360px,520px)]'>
            <div className='rounded-md border'>
              {loading && (
                <p className='text-muted-foreground p-4 text-sm'>
                  {t('Loading...')}
                </p>
              )}
              {!loading && routes.length === 0 && (
                <p className='text-muted-foreground p-4 text-sm'>
                  {t('No client model routes')}
                </p>
              )}
              {!loading && routes.length > 0 && (
                <div className='divide-y'>
                  {routes.map((route) => (
                    <div
                      className='flex items-start justify-between gap-3 p-4'
                      key={route.id}
                    >
                      <div className='min-w-0'>
                        <p className='font-medium'>{route.public_id}</p>
                        <p className='text-muted-foreground text-xs'>
                          {t('Aliases')}: {route.aliases.join(', ') || '—'} ·{' '}
                          {t('Client')}: {route.client} ·{' '}
                          {route.enabled ? t('Enabled') : t('Disabled')}
                        </p>
                        <p className='text-muted-foreground mt-1 text-xs'>
                          {t('{{count}} targets', {
                            count: route.targets.length,
                          })}
                          :{' '}
                          {route.targets
                            .map((target) => {
                              const channel = channelsByID.get(
                                target.channel_id
                              )
                              return `${channel?.name ?? `#${target.channel_id}`}/${target.upstream_model}`
                            })
                            .join(', ')}
                        </p>
                      </div>
                      <div className='flex shrink-0 gap-2'>
                        <Button
                          size='sm'
                          variant='outline'
                          onClick={() => edit(route)}
                        >
                          {t('Edit')}
                        </Button>
                        <Button
                          size='sm'
                          variant='destructive'
                          onClick={() => setDeletingRoute(route)}
                        >
                          {t('Delete')}
                        </Button>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>

            <div className='space-y-3 rounded-md border p-4'>
              <h3 className='font-semibold'>
                {form.id ? t('Edit route') : t('New route')}
              </h3>
              <TextField
                label={t('Public ID')}
                value={form.public_id}
                onChange={(value) => setForm({ ...form, public_id: value })}
                required
              />
              <TextField
                label={t('Display name')}
                value={form.display_name}
                onChange={(value) => setForm({ ...form, display_name: value })}
              />
              <label className='block text-sm'>
                {t('Client scope')}
                <select
                  className='mt-1 w-full rounded border p-2'
                  value={form.client}
                  onChange={(event) =>
                    setForm({ ...form, client: event.target.value })
                  }
                >
                  <option value='all'>{t('All clients')}</option>
                  <option value='codex'>Codex</option>
                  <option value='claude'>Claude Code</option>
                  <option value='gemini'>Gemini</option>
                </select>
              </label>
              <TextField
                label={t('Aliases (comma separated)')}
                value={form.aliases.join(', ')}
                onChange={(value) =>
                  setForm({
                    ...form,
                    aliases: value
                      .split(',')
                      .map((alias) => alias.trim())
                      .filter(Boolean),
                  })
                }
              />
              <label className='block text-sm'>
                {t('Metadata (JSON)')}
                <textarea
                  className='mt-1 w-full rounded border p-2 font-mono text-xs'
                  rows={2}
                  value={form.metadata || ''}
                  onChange={(event) =>
                    setForm({ ...form, metadata: event.target.value })
                  }
                />
              </label>

              {form.targets.map((target, index) => {
                const channel = channelsByID.get(target.channel_id)
                const selectedWires = parsePreferredWires(
                  target.preferred_wires
                )
                return (
                  <div
                    className='space-y-3 rounded border p-3'
                    key={target.id ?? index}
                  >
                    <div className='flex items-center justify-between'>
                      <span className='text-sm font-medium'>
                        {t('Target {{number}}', { number: index + 1 })}
                      </span>
                      <Button
                        size='sm'
                        variant='ghost'
                        disabled={form.targets.length === 1}
                        onClick={() =>
                          setForm({
                            ...form,
                            targets: form.targets.filter(
                              (_, targetIndex) => targetIndex !== index
                            ),
                          })
                        }
                      >
                        {t('Remove')}
                      </Button>
                    </div>
                    <label className='block text-sm'>
                      {t('Channel')}
                      <select
                        className='mt-1 w-full rounded border p-2'
                        value={target.channel_id || ''}
                        onChange={(event) => {
                          const nextChannel = channelsByID.get(
                            Number(event.target.value)
                          )
                          setTarget(form, setForm, index, {
                            channel_id: nextChannel?.id ?? 0,
                            upstream_model: nextChannel?.models[0] ?? '',
                            preferred_wires: serializePreferredWires(
                              nextChannel?.wires ?? []
                            ),
                          })
                        }}
                      >
                        <option value=''>{t('Select a channel')}</option>
                        {channels.map((option) => (
                          <option key={option.id} value={option.id}>
                            {option.name} (#{option.id})
                          </option>
                        ))}
                      </select>
                    </label>
                    <div className='grid grid-cols-2 gap-2'>
                      <label className='text-sm'>
                        {t('Upstream model')}
                        <select
                          className='mt-1 w-full rounded border p-2'
                          disabled={!channel}
                          value={target.upstream_model}
                          onChange={(event) =>
                            setTarget(form, setForm, index, {
                              upstream_model: event.target.value,
                            })
                          }
                        >
                          {!channel && <option value='' />}
                          {channel &&
                            uniqueOptions(
                              channel.models,
                              target.upstream_model
                            ).map((modelName) => (
                              <option key={modelName} value={modelName}>
                                {modelName}
                              </option>
                            ))}
                        </select>
                      </label>
                      <label className='text-sm'>
                        {t('Billing model')}
                        <select
                          className='mt-1 w-full rounded border p-2'
                          value={target.billing_model}
                          onChange={(event) =>
                            setTarget(form, setForm, index, {
                              billing_model: event.target.value,
                            })
                          }
                        >
                          <option value=''>{t('Select billing model')}</option>
                          {uniqueOptions(
                            billingModels,
                            target.billing_model
                          ).map((modelName) => (
                            <option key={modelName} value={modelName}>
                              {modelName}
                            </option>
                          ))}
                        </select>
                      </label>
                      <NumberField
                        label={t('Priority')}
                        value={target.priority}
                        onChange={(value) =>
                          setTarget(form, setForm, index, { priority: value })
                        }
                      />
                      <NumberField
                        label={t('Weight')}
                        value={target.weight}
                        min={1}
                        onChange={(value) =>
                          setTarget(form, setForm, index, { weight: value })
                        }
                      />
                    </div>
                    <fieldset>
                      <legend className='text-sm'>
                        {t('Preferred protocols')}
                      </legend>
                      <div className='mt-2 flex flex-wrap gap-4'>
                        {WIRE_PROTOCOLS.map((wire) => (
                          <label
                            className='flex items-center gap-2 text-sm'
                            key={wire}
                          >
                            <Checkbox
                              checked={selectedWires.includes(wire)}
                              disabled={
                                !!channel && !channel.wires.includes(wire)
                              }
                              onCheckedChange={(checked) => {
                                const nextWires = checked
                                  ? [...selectedWires, wire]
                                  : selectedWires.filter(
                                      (selected) => selected !== wire
                                    )
                                setTarget(form, setForm, index, {
                                  preferred_wires:
                                    serializePreferredWires(nextWires),
                                })
                              }}
                            />
                            {t(wireLabelKey(wire))}
                          </label>
                        ))}
                      </div>
                    </fieldset>
                    <label className='flex items-center gap-2 text-sm'>
                      <Checkbox
                        checked={target.enabled}
                        onCheckedChange={(checked) =>
                          setTarget(form, setForm, index, {
                            enabled: checked === true,
                          })
                        }
                      />
                      {t('Target enabled')}
                    </label>
                  </div>
                )
              })}
              <Button
                variant='outline'
                onClick={() =>
                  setForm({
                    ...form,
                    targets: [...form.targets, emptyTarget()],
                  })
                }
              >
                {t('Add target')}
              </Button>
              <label className='flex items-center gap-2 text-sm'>
                <Checkbox
                  checked={form.enabled}
                  onCheckedChange={(checked) =>
                    setForm({ ...form, enabled: checked === true })
                  }
                />
                {t('Route enabled')}
              </label>
              {error && (
                <p role='alert' className='text-destructive text-sm'>
                  {error}
                </p>
              )}
              <Button
                disabled={loading || saving}
                onClick={() => void submit()}
              >
                {saving ? t('Saving...') : t('Save route')}
              </Button>
            </div>
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <ConfirmDialog
        open={deletingRoute !== null}
        onOpenChange={(open) => {
          if (!open && !deleting) setDeletingRoute(null)
        }}
        title={t('Delete client model route')}
        desc={t('Delete route {{name}}? This action cannot be undone.', {
          name: deletingRoute?.public_id ?? '',
        })}
        destructive
        isLoading={deleting}
        confirmText={t('Delete')}
        handleConfirm={() => void remove()}
      />
    </>
  )
}

type TextFieldProps = {
  label: string
  value: string
  onChange: (value: string) => void
  required?: boolean
}

function TextField({ label, value, onChange, required }: TextFieldProps) {
  return (
    <label className='block text-sm'>
      {label}
      <input
        className='mt-1 w-full rounded border p-2'
        value={value}
        required={required}
        onChange={(event) => onChange(event.target.value)}
      />
    </label>
  )
}

type NumberFieldProps = {
  label: string
  value: number
  min?: number
  onChange: (value: number) => void
}

function NumberField({ label, value, min, onChange }: NumberFieldProps) {
  return (
    <label className='text-sm'>
      {label}
      <input
        type='number'
        min={min}
        className='mt-1 w-full rounded border p-2'
        value={value}
        onChange={(event) => onChange(Number(event.target.value))}
      />
    </label>
  )
}

function setTarget(
  form: ClientModelRouteInput,
  setForm: (next: ClientModelRouteInput) => void,
  index: number,
  patch: Partial<ClientModelTarget>
) {
  setForm({
    ...form,
    targets: form.targets.map((target, targetIndex) =>
      targetIndex === index ? { ...target, ...patch } : target
    ),
  })
}

function uniqueOptions(values: string[], current: string): string[] {
  return [...new Set([...values, current].filter(Boolean))]
}

function wireLabelKey(wire: WireProtocol): string {
  switch (wire) {
    case 'responses':
      return 'Responses'
    case 'openai_chat':
      return 'OpenAI Chat'
    case 'anthropic':
      return 'Anthropic Messages'
  }
}
