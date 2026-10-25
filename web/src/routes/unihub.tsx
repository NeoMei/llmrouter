import { createFileRoute } from '@tanstack/react-router'
import { lazy, Suspense } from 'react'

const UniHub = lazy(() =>
  import('@/features/unihub').then((module) => ({ default: module.UniHub }))
)
export const Route = createFileRoute('/unihub')({
  component: () => (
    <Suspense fallback={<p>UniHub…</p>}>
      <UniHub />
    </Suspense>
  ),
})
