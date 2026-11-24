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
import { SendIcon, SquareIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { PromptInputButton } from '@/components/ai-elements/prompt-input'
import { ModelGroupSelector } from '@/components/model-group-selector'

import { getInputControlState } from '../../lib'
import type { GroupOption, ModelOption } from '../../types'

type PlaygroundInputControlsProps = {
  disabled?: boolean
  groups: GroupOption[]
  groupValue: string
  isGenerating?: boolean
  isModelLoading?: boolean
  models: ModelOption[]
  modelValue: string
  onGroupChange: (value: string) => void
  onModelChange: (value: string) => void
  onStop?: () => void
  text: string
  tools: ReactNode
}

export function PlaygroundInputControls({
  disabled,
  groups,
  groupValue,
  isGenerating,
  isModelLoading = false,
  models,
  modelValue,
  onGroupChange,
  onModelChange,
  onStop,
  text,
  tools,
}: PlaygroundInputControlsProps) {
  const { t } = useTranslation()
  const { canSubmit, isSelectorDisabled, shouldShowStop } =
    getInputControlState({
      disabled,
      groups,
      hasStopHandler: Boolean(onStop),
      isGenerating,
      isModelLoading,
      models,
      text,
    })

  return (
    <div className='flex w-full flex-wrap items-center gap-2'>
      <div className='flex min-w-0 items-center gap-2'>{tools}</div>
      <div className='ms-auto flex min-w-0 items-center gap-2'>
        <ModelGroupSelector
          selectedModel={modelValue}
          models={models}
          onModelChange={onModelChange}
          selectedGroup={groupValue}
          groups={groups}
          onGroupChange={onGroupChange}
          disabled={isSelectorDisabled}
        />
        {shouldShowStop ? (
          <PromptInputButton
            className='border-destructive/25 bg-destructive/10 text-destructive hover:bg-destructive/15 font-medium'
            onClick={onStop}
            variant='secondary'
          >
            <SquareIcon className='fill-current' size={16} />
            {t('Stop')}
          </PromptInputButton>
        ) : (
          <PromptInputButton
            className='bg-primary text-primary-foreground hover:bg-primary/90 disabled:bg-muted disabled:text-muted-foreground h-8 px-3 font-medium shadow-sm'
            disabled={!canSubmit}
            type='submit'
            variant='default'
          >
            <SendIcon size={16} />
            {t('Send')}
          </PromptInputButton>
        )}
      </div>
    </div>
  )
}
