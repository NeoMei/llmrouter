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
import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'

import { ConfirmDialog } from '@/components/confirm-dialog'

export const Route = createFileRoute('/diag')({
  component: DiagComponent,
})

function DiagComponent() {
  const [open, setOpen] = useState(false)
  return (
    <div className='p-8'>
      <h1>Diag</h1>
      <button
        type='button'
        id='diag-toggle'
        className='rounded border p-2'
        onClick={() => setOpen(true)}
      >
        Open Dialog
      </button>
      <div id='diag-state'>open={String(open)}</div>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title='Diag Title'
        desc='Diag description here'
        confirmText='OK'
        handleConfirm={() => setOpen(false)}
      />
    </div>
  )
}
