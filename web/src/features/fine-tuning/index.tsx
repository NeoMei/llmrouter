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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { getCapabilities } from '@/features/capabilities/api'
import {
  getSupportingCapabilities,
  hasCapability,
} from '@/features/capabilities/types'
import { listFiles } from '@/features/files/api'

import {
  cancelFineTuningJob,
  createFineTuningJob,
  deleteFineTunedModel,
  isFineTuningTerminal,
  listFineTuningEvents,
  listFineTuningJobs,
} from './api'

export function FineTuningPage() {
  const queryClient = useQueryClient()
  const [trainingFile, setTrainingFile] = useState('')
  const [validationFile, setValidationFile] = useState('')
  const [model, setModel] = useState('gpt-4.1-mini')
  const [suffix, setSuffix] = useState('')
  const [epochs, setEpochs] = useState('')
  const [selectedJob, setSelectedJob] = useState('')
  const jobs = useQuery({
    queryKey: ['fine-tuning-jobs'],
    queryFn: listFineTuningJobs,
    refetchInterval: 15_000,
  })
  const files = useQuery({ queryKey: ['api-files'], queryFn: listFiles })
  const capabilities = useQuery({
    queryKey: ['capabilities'],
    queryFn: getCapabilities,
    staleTime: 300_000,
  })
  const events = useQuery({
    queryKey: ['fine-tuning-events', selectedJob],
    queryFn: () => listFineTuningEvents(selectedJob),
    enabled: Boolean(selectedJob),
    refetchInterval: 15_000,
  })
  const create = useMutation({
    mutationFn: createFineTuningJob,
    onSuccess: (job) => {
      setSelectedJob(job.id)
      queryClient.invalidateQueries({ queryKey: ['fine-tuning-jobs'] })
    },
  })
  const cancel = useMutation({
    mutationFn: cancelFineTuningJob,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ['fine-tuning-jobs'] }),
  })
  const removeModel = useMutation({
    mutationFn: deleteFineTunedModel,
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ['fine-tuning-jobs'] }),
  })
  const canCreate = hasCapability(capabilities.data, 'fine_tuning.jobs.create')
  const canCancel = hasCapability(capabilities.data, 'fine_tuning.jobs.cancel')
  const canDeleteModel = hasCapability(capabilities.data, 'models.delete')
  const providers = getSupportingCapabilities(
    capabilities.data,
    'fine_tuning.jobs.create'
  )
  const fineTuningFiles = (files.data ?? []).filter(
    (file) => file.purpose === 'fine-tune' && file.status === 'ready'
  )
  const selected = jobs.data?.find((job) => job.id === selectedJob)
  const mutationError = [create.error, cancel.error, removeModel.error].find(
    (error) => error instanceof Error
  )

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>Fine-tuning</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        {canCreate && (
          <div className='mb-6 space-y-3 rounded-lg border p-4'>
            <div className='grid gap-3 md:grid-cols-2'>
              <select
                aria-label='Training file'
                className='h-9 rounded-md border bg-transparent px-3'
                value={trainingFile}
                onChange={(event) => setTrainingFile(event.target.value)}
              >
                <option value=''>Select training file</option>
                {fineTuningFiles.map((file) => (
                  <option key={file.id} value={file.id}>
                    {file.filename}
                  </option>
                ))}
              </select>
              <select
                aria-label='Validation file'
                className='h-9 rounded-md border bg-transparent px-3'
                value={validationFile}
                onChange={(event) => setValidationFile(event.target.value)}
              >
                <option value=''>No validation file</option>
                {fineTuningFiles
                  .filter((file) => file.id !== trainingFile)
                  .map((file) => (
                    <option key={file.id} value={file.id}>
                      {file.filename}
                    </option>
                  ))}
              </select>
              <Input
                value={model}
                onChange={(event) => setModel(event.target.value)}
                placeholder='Base model'
              />
              <Input
                value={suffix}
                maxLength={64}
                onChange={(event) => setSuffix(event.target.value)}
                placeholder='Suffix (optional)'
              />
              <Input
                value={epochs}
                min={1}
                type='number'
                onChange={(event) => setEpochs(event.target.value)}
                placeholder='Epochs (provider default)'
              />
              <Button
                disabled={!trainingFile || !model.trim() || create.isPending}
                onClick={() =>
                  create.mutate({
                    training_file: trainingFile,
                    validation_file: validationFile || undefined,
                    model: model.trim(),
                    suffix: suffix.trim() || undefined,
                    n_epochs: epochs ? Number(epochs) : undefined,
                  })
                }
              >
                {create.isPending ? 'Creating…' : 'Create job'}
              </Button>
            </div>
            <p className='text-muted-foreground text-xs'>
              Declared providers:{' '}
              {providers
                .map(
                  (provider) =>
                    `${provider.channel_name} (${provider.protocol})`
                )
                .join(', ') || 'none'}
              {' · '}registry {capabilities.data?.version ?? 'loading'}
            </p>
          </div>
        )}
        {!capabilities.isPending && !canCreate && (
          <p className='text-muted-foreground mb-4 text-sm'>
            Job creation is unavailable because no configured provider declares
            fine-tuning support.
          </p>
        )}
        {(jobs.error instanceof Error || mutationError instanceof Error) && (
          <p className='text-destructive mb-4'>
            {mutationError?.message || jobs.error?.message}
          </p>
        )}
        <div className='grid gap-4 lg:grid-cols-2'>
          <div className='space-y-2'>
            {(jobs.data ?? []).map((job) => (
              <div
                key={job.id}
                className='flex w-full items-center justify-between rounded-lg border p-3'
              >
                <button
                  type='button'
                  className='min-w-0 flex-1 text-left'
                  onClick={() => setSelectedJob(job.id)}
                >
                  <span className='block font-medium'>{job.model}</span>
                  <span className='text-muted-foreground block truncate text-xs'>
                    {job.id}
                  </span>
                </button>
                <span className='ml-3 flex items-center gap-2 text-sm'>
                  {job.status}
                  {canCancel && !isFineTuningTerminal(job.status) && (
                    <Button
                      size='sm'
                      variant='outline'
                      onClick={() => cancel.mutate(job.id)}
                    >
                      Cancel
                    </Button>
                  )}
                </span>
              </div>
            ))}
          </div>
          <div className='space-y-4 rounded-lg border p-4'>
            <div>
              <h3 className='mb-3 font-medium'>Job details</h3>
              {selected ? (
                <dl className='grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm'>
                  <dt className='text-muted-foreground'>Status</dt>
                  <dd>{selected.status}</dd>
                  <dt className='text-muted-foreground'>Training file</dt>
                  <dd className='truncate'>{selected.training_file}</dd>
                  <dt className='text-muted-foreground'>Validation file</dt>
                  <dd className='truncate'>
                    {selected.validation_file || '—'}
                  </dd>
                  <dt className='text-muted-foreground'>Created</dt>
                  <dd>
                    {new Date(selected.created_at * 1000).toLocaleString()}
                  </dd>
                  <dt className='text-muted-foreground'>Result model</dt>
                  <dd className='flex items-center gap-2'>
                    <span className='truncate'>
                      {selected.fine_tuned_model || '—'}
                    </span>
                    {canDeleteModel && selected.fine_tuned_model && (
                      <Button
                        size='sm'
                        variant='outline'
                        disabled={removeModel.isPending}
                        onClick={() => {
                          if (selected.fine_tuned_model) {
                            removeModel.mutate(selected.fine_tuned_model)
                          }
                        }}
                      >
                        Delete model
                      </Button>
                    )}
                  </dd>
                  {selected.error !== undefined && (
                    <>
                      <dt className='text-destructive'>Error</dt>
                      <dd className='text-destructive break-all'>
                        {typeof selected.error === 'string'
                          ? selected.error
                          : JSON.stringify(selected.error)}
                      </dd>
                    </>
                  )}
                </dl>
              ) : (
                <p className='text-muted-foreground text-sm'>Select a job.</p>
              )}
            </div>
            <div>
              <h3 className='mb-3 font-medium'>Events</h3>
              {(events.data ?? []).map((event) => (
                <div
                  key={`${event.created_at}:${event.message}`}
                  className='border-b py-2 text-sm'
                >
                  <span className='text-muted-foreground mr-2'>
                    {new Date(event.created_at * 1000).toLocaleString()} ·{' '}
                    {event.level}
                  </span>
                  {event.message}
                </div>
              ))}
              {selectedJob && !events.isPending && !events.data?.length && (
                <p className='text-muted-foreground text-sm'>No events.</p>
              )}
            </div>
          </div>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
