import type { Page } from '@playwright/test'

export async function mockDesktopSession(page: Page) {
  const now = Math.floor(Date.now() / 1000)
  const user = { id: 1, username: 'desktop-fixture', role: 100, status: 1 }
  await page.addInitScript(() => {
    localStorage.setItem('setup_status_checked', 'true')
    localStorage.setItem('i18nextLng', 'en')
  })
  await page.route('**/api/**', (route) => {
    const path = new URL(route.request().url()).pathname
    const responses: Record<string, unknown> = {
      '/api/user/auth/refresh': {
        access_token: 'desktop-fixture',
        token_type: 'Bearer',
        access_expires_at: now + 3600,
        user,
        session: {
          sid: 'desktop-fixture',
          current: true,
          login_method: 'password',
          ip: '127.0.0.1',
          user_agent: 'desktop-fixture',
          created_at: now,
          last_active_at: now,
          expires_at: now + 3600,
        },
      },
      '/api/user/self': user,
      '/api/status': { system_name: 'LLMRouter Desktop Fixture' },
      '/api/notice': '',
      '/api/token/': { items: [], total: 0, page: 1, page_size: 10 },
      '/api/user/self/groups': { default: { desc: 'Default', ratio: 1 } },
    }

    return route.fulfill({
      json: { success: true, data: responses[path] ?? [] },
    })
  })
}
