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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

import { patchMetadata, readMetadata } from './metadata'

// These are supported paths, not a promise of provider or purchased-account entitlement.
const operations = [
  'chat.completions.create',
  'responses.create',
  'embeddings.create',
  'multimodal.embeddings.create',
  'audio.transcriptions.create',
  'audio.speech.create',
  'images.generations.create',
  'images.edits.create',
  'ocr.jobs.create',
  'ocr.jobs.retrieve',
]
const mediaNumbers = [
  'max_request_bytes',
  'max_images',
  'max_audio_seconds',
  'max_video_seconds',
  'max_pages',
  'max_pixels',
]
const spaceStrings = ['provider', 'model', 'version', 'input_semantics']

export function MetadataEditor(props: {
  value: string
  onChange: (value: string) => void
  target?: boolean
  lockedSpace?: boolean
}) {
  const { t } = useTranslation()
  const [patchError, setPatchError] = useState(false)
  let data: Record<string, unknown> = {}
  let valid = true
  try {
    data = readMetadata(props.value)
  } catch {
    valid = false
  }
  const patch = (path: string[], value: unknown) => {
    try {
      props.onChange(patchMetadata(props.value, path, value))
      setPatchError(false)
    } catch {
      setPatchError(true)
    }
  }
  const media = data.media_limits as Record<string, unknown> | undefined
  const space = data.embedding_space as Record<string, unknown> | undefined
  return (
    <section
      className='space-y-3 rounded border p-3'
      aria-label={
        props.target ? t('Target embedding metadata') : t('Model capabilities')
      }
    >
      <h3 className='text-sm font-medium'>
        {props.target
          ? t('Target embedding metadata')
          : t('Model capabilities')}
      </h3>
      <label className='block text-sm'>
        {props.target ? t('Target metadata (JSON)') : t('Metadata (JSON)')}
        <Textarea
          className='mt-1 font-mono text-xs'
          rows={3}
          value={props.value}
          onChange={(event) => {
            props.onChange(event.target.value)
            setPatchError(false)
          }}
        />
      </label>
      {(!valid || patchError) && (
        <Alert variant='destructive'>
          <AlertDescription>
            {t('Metadata must be valid JSON with unique fields')}
          </AlertDescription>
        </Alert>
      )}
      <fieldset disabled={!valid} className='space-y-3'>
        {!props.target && (
          <>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Capabilities are operator declarations, not purchased entitlement. Server validation remains authoritative. Secrets belong in channel configuration.'
              )}
            </p>
            <div className='grid gap-2 sm:grid-cols-2'>
              {['input_modalities', 'output_modalities', 'operations'].map(
                (key) => (
                  <label className='text-xs' key={key}>
                    {key}
                    <Input
                      list={
                        key === 'operations'
                          ? 'metadata-operations'
                          : 'metadata-modalities'
                      }
                      value={
                        Array.isArray(data[key])
                          ? (data[key] as string[]).join(', ')
                          : ''
                      }
                      onChange={(event) =>
                        patch(
                          [key],
                          event.target.value
                            ? event.target.value
                                .split(',')
                                .map((value) => value.trim())
                            : undefined
                        )
                      }
                    />
                  </label>
                )
              )}
              {['streaming', 'async'].map((key) => (
                <OptionalBoolean
                  key={key}
                  name={key}
                  value={data[key]}
                  onChange={(value) => patch([key], value)}
                />
              ))}
            </div>
            <datalist id='metadata-modalities'>
              {['text', 'image', 'audio', 'video', 'document', 'embedding'].map(
                (value) => (
                  <option key={value} value={value} />
                )
              )}
            </datalist>
            <datalist id='metadata-operations'>
              {operations.map((value) => (
                <option key={value} value={value} />
              ))}
            </datalist>
            <details>
              <summary className='cursor-pointer text-sm'>
                {t('Media limits')}
              </summary>
              <div className='mt-2 grid gap-2 sm:grid-cols-2'>
                {mediaNumbers.map((key) => (
                  <label key={key} className='text-xs'>
                    {key}
                    <Input
                      type='number'
                      value={
                        typeof media?.[key] === 'number'
                          ? String(media[key])
                          : ''
                      }
                      onChange={(event) =>
                        patch(
                          ['media_limits', key],
                          event.target.value === ''
                            ? undefined
                            : Number(event.target.value)
                        )
                      }
                    />
                  </label>
                ))}
                <label className='text-xs'>
                  formats
                  <Input
                    value={
                      Array.isArray(media?.formats)
                        ? (media.formats as string[]).join(', ')
                        : ''
                    }
                    onChange={(event) =>
                      patch(
                        ['media_limits', 'formats'],
                        event.target.value
                          ? event.target.value
                              .split(',')
                              .map((value) => value.trim())
                          : undefined
                      )
                    }
                  />
                </label>
                <OptionalBoolean
                  name='video_audio'
                  value={media?.video_audio}
                  onChange={(value) =>
                    patch(['media_limits', 'video_audio'], value)
                  }
                />
              </div>
            </details>
          </>
        )}
        <details open={!!space}>
          <summary className='cursor-pointer text-sm'>
            {t('Fixed embedding identity')}
          </summary>
          <p className='text-muted-foreground my-2 text-xs'>
            {t(
              'Changed vector spaces require a new public route and disjoint aliases'
            )}
          </p>
          <fieldset
            disabled={props.lockedSpace}
            className='grid gap-2 sm:grid-cols-2'
          >
            {spaceStrings.map((key) => (
              <label className='text-xs' key={key}>
                {key}
                <Input
                  value={
                    typeof space?.[key] === 'string' ? String(space[key]) : ''
                  }
                  onChange={(event) =>
                    patch(['embedding_space', key], event.target.value)
                  }
                />
              </label>
            ))}
            <label className='text-xs'>
              dimensions
              <Input
                type='number'
                min={1}
                max={65536}
                value={
                  typeof space?.dimensions === 'number'
                    ? String(space.dimensions)
                    : ''
                }
                onChange={(event) =>
                  patch(
                    ['embedding_space', 'dimensions'],
                    event.target.value === ''
                      ? undefined
                      : Number(event.target.value)
                  )
                }
              />
            </label>
            {space && (
              <Button
                variant='outline'
                size='sm'
                onClick={() => patch(['embedding_space'], undefined)}
              >
                {t('Remove declaration')}
              </Button>
            )}
          </fieldset>
        </details>
      </fieldset>
      {!props.target && (
        <details>
          <summary className='cursor-pointer text-sm'>
            {t('Supported paths and prerequisites')}
          </summary>
          <div className='text-muted-foreground space-y-2 text-xs'>
            <p>
              {t(
                'OCR uses paddleocr_jobs: create and retrieve are both needed for submit and poll. Native channel 60 has no generic chat connection test. Configure models and per-request price explicitly; OCR public origin and persistent grant secret are server prerequisites.'
              )}
            </p>
            <p>
              {t(
                'Qwen multimodal vectors require qwen3-vl-embedding, a fixed space, independent PNG/JPEG and H264 MP4 without audio at fps=1. Visual requests require a configured reservation ceiling; video also requires the media runtime. New spaces never migrate existing indexes.'
              )}
            </p>
            <p>
              {t(
                'Understanding is separate from generation. Selected Qwen Omni is nonstreaming text output; ASR accepts PCM WAV. MiniMax speech uses configured per-request pricing and selected system voices; binary streaming is MP3 only. Qwen image generation and editing are synchronous with configured per-image pricing.'
              )}
            </p>
          </div>
        </details>
      )}
    </section>
  )
}

function OptionalBoolean(props: {
  name: string
  value: unknown
  onChange: (value: boolean | undefined) => void
}) {
  const { t } = useTranslation()
  return (
    <label className='text-xs'>
      {props.name}
      <select
        aria-label={props.name}
        className='mt-1 w-full rounded border p-2'
        value={props.value === undefined ? '' : String(props.value)}
        onChange={(event) =>
          props.onChange(
            event.target.value === ''
              ? undefined
              : event.target.value === 'true'
          )
        }
      >
        <option value=''>{t('Not declared')}</option>
        <option value='true'>{t('Yes')}</option>
        <option value='false'>{t('No')}</option>
      </select>
    </label>
  )
}
