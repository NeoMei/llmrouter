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
import { Download, Trash2, Upload } from 'lucide-react'
import { useState } from 'react'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { getCapabilities } from '@/features/capabilities/api'
import { hasCapability } from '@/features/capabilities/types'

import {
  deleteFile,
  downloadFile,
  listFiles,
  uploadFile,
  validateUpload,
} from './api'

export function FilesPage() {
  const queryClient = useQueryClient()
  const [error, setError] = useState('')
  const [purpose, setPurpose] = useState('fine-tune')
  const [progress, setProgress] = useState(0)
  const files = useQuery({ queryKey: ['api-files'], queryFn: listFiles })
  const capabilities = useQuery({
    queryKey: ['capabilities'],
    queryFn: getCapabilities,
    staleTime: 300_000,
  })
  const upload = useMutation({
    mutationFn: (file: File) => uploadFile(file, purpose, setProgress),
    onMutate: () => setProgress(0),
    onSuccess: () => {
      setProgress(100)
      return queryClient.invalidateQueries({ queryKey: ['api-files'] })
    },
  })
  const remove = useMutation({
    mutationFn: deleteFile,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['api-files'] }),
  })
  const canUpload = hasCapability(capabilities.data, 'files.create')
  const canDownload = hasCapability(capabilities.data, 'files.content')
  const canDelete = hasCapability(capabilities.data, 'files.delete')

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>Files</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        {canUpload && (
          <select
            aria-label='File purpose'
            className='h-9 rounded-md border bg-transparent px-3 text-sm'
            value={purpose}
            onChange={(event) => setPurpose(event.target.value)}
          >
            <option value='fine-tune'>Fine-tuning</option>
            <option value='assistants'>Assistants</option>
            <option value='batch'>Batch</option>
          </select>
        )}
        {canUpload && (
          <label className='inline-flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-50'>
            <Upload className='size-4' />
            {upload.isPending ? `Uploading ${progress}%` : 'Upload'}
            <input
              className='sr-only'
              type='file'
              disabled={upload.isPending}
              accept='.jsonl,.json,.txt,.csv'
              onChange={(event) => {
                const selected = event.target.files?.[0]
                if (!selected) {
                  return
                }
                const message = validateUpload(selected)
                setError(message ?? '')
                if (!message) upload.mutate(selected)
              }}
            />
          </label>
        )}
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        {!capabilities.isPending && !canUpload && (
          <p className='text-muted-foreground mb-4 text-sm'>
            Upload is unavailable because no configured provider declares file
            creation support.
          </p>
        )}
        {(error || upload.error instanceof Error) && (
          <p className='text-destructive mb-4'>
            {error || upload.error?.message}
          </p>
        )}
        {files.error instanceof Error && (
          <p className='text-destructive mb-4'>{files.error.message}</p>
        )}
        <div className='space-y-2'>
          {(files.data ?? []).map((file) => (
            <div
              key={file.id}
              className='flex items-center justify-between rounded-lg border p-3'
            >
              <div>
                <p className='font-medium'>{file.filename}</p>
                <p className='text-muted-foreground text-xs'>
                  {file.bytes.toLocaleString()} bytes · {file.purpose} ·{' '}
                  {file.status} ·{' '}
                  {new Date(file.created_at * 1000).toLocaleString()}
                </p>
              </div>
              <div className='flex gap-2'>
                {canDownload && (
                  <Button
                    aria-label={`Download ${file.filename}`}
                    size='sm'
                    variant='outline'
                    onClick={() => {
                      void downloadFile(file)
                    }}
                  >
                    <Download className='size-4' />
                  </Button>
                )}
                {canDelete && (
                  <Button
                    aria-label={`Delete ${file.filename}`}
                    size='sm'
                    variant='outline'
                    disabled={remove.isPending}
                    onClick={() => remove.mutate(file.id)}
                  >
                    <Trash2 className='size-4' />
                  </Button>
                )}
              </div>
            </div>
          ))}
          {!files.isPending && (files.data?.length ?? 0) === 0 && (
            <p className='text-muted-foreground'>No files uploaded.</p>
          )}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
