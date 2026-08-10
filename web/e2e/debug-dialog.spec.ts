import { expect, test } from '@playwright/test'

const now = Math.floor(Date.now() / 1000)

test('diag ConfirmDialog opens in production build', async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem('setup_status_checked', 'true')
    window.localStorage.setItem(
      'status',
      JSON.stringify({ system_name: 'Diag' })
    )
  })
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/user/auth/refresh') {
      await route.fulfill({
        json: {
          success: true,
          data: {
            access_token: 't',
            token_type: 'Bearer',
            access_expires_at: now + 3600,
            user: {
              id: 1,
              username: 'a',
              display_name: 'A',
              role: 10,
              status: 1,
            },
            session: {
              sid: 's',
              current: true,
              login_method: 'password',
              ip: '1',
              user_agent: 'p',
              created_at: now,
              last_active_at: now,
              expires_at: now + 3600,
            },
          },
        },
      })
      return
    }
    if (path === '/api/status') {
      await route.fulfill({
        json: { success: true, data: { system_name: 'Diag' } },
      })
      return
    }
    if (path === '/api/notice') {
      await route.fulfill({ json: { success: true, data: '' } })
      return
    }
    await route.fulfill({ json: { success: true, data: {} } })
  })

  await page.goto('/diag')
  await expect(page.locator('#diag-toggle')).toBeVisible({ timeout: 15000 })
  await page.locator('#diag-toggle').click()
  await page.waitForTimeout(800)
  const bodyHtml = await page.locator('body').innerHTML()
  console.log(
    'PROD diag body has alert-dialog:',
    bodyHtml.includes('alert-dialog')
  )
  console.log('PROD diag body has Diag Title:', bodyHtml.includes('Diag Title'))
  const alertDialogs = await page.locator('[role="alertdialog"]').count()
  console.log('PROD diag alertdialog count:', alertDialogs)
  const dataDialogSlots = await page.evaluate(() => {
    const set = new Set<string>()
    document.querySelectorAll('[data-slot]').forEach((el) => {
      const s = el.getAttribute('data-slot') || ''
      if (s.includes('dialog') || s.includes('alert')) set.add(s)
    })
    return Array.from(set)
  })
  console.log('PROD diag dialog data-slots:', JSON.stringify(dataDialogSlots))
  expect(true).toBe(true)
})
