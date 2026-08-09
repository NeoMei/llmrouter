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
import { api } from '@/lib/api'

export type APIFile = {
  id: string
  object: 'file'
  bytes: number
  created_at: number
  filename: string
  purpose: string
  status: string
}

export async function listFiles(): Promise<APIFile[]> {
  const response = await api.get('/v1/files')
  return (response.data?.data ?? []) as APIFile[]
}

export async function uploadFile(
  file: File,
  purpose: string,
  onProgress?: (percent: number) => void
): Promise<APIFile> {
  const body = new FormData()
  body.append('purpose', purpose)
  body.append('file', file)
  const response = await api.post('/v1/files', body, {
    onUploadProgress: (event) => {
      if (event.total && onProgress) {
        onProgress(
          Math.min(100, Math.round((event.loaded / event.total) * 100))
        )
      }
    },
  })
  return response.data as APIFile
}

export async function deleteFile(id: string): Promise<void> {
  await api.delete(`/v1/files/${encodeURIComponent(id)}`)
}

export async function downloadFile(file: APIFile): Promise<void> {
  const response = await api.get(
    `/v1/files/${encodeURIComponent(file.id)}/content`,
    { responseType: 'blob' }
  )
  const href = URL.createObjectURL(response.data as Blob)
  const anchor = document.createElement('a')
  anchor.href = href
  anchor.download = file.filename
  anchor.click()
  URL.revokeObjectURL(href)
}

export function validateUpload(
  file: Pick<File, 'name' | 'size'>
): string | undefined {
  if (file.size <= 0) {
    return 'File is empty'
  }
  if (file.size > 100 * 1024 * 1024) {
    return 'File exceeds 100 MiB'
  }
  if (!/\.(jsonl|json|txt|csv)$/i.test(file.name)) {
    return 'Unsupported file type'
  }
  return undefined
}
