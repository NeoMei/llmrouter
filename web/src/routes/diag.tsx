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
