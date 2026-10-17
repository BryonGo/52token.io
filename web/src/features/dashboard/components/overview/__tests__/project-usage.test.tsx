import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { ProjectUsagePanel } from '../project-usage-panel'

const i18n = createInstance()
await i18n.init({
  lng: 'en',
  resources: { en: { translation: {} }, zh },
  initAsync: false,
})
let client: QueryClient
const report = {
  total_quota: 5000000,
  period_quota: 1500000,
  requests: 3,
  timezone: 'Asia/Shanghai',
  daily: [
    { name: '2026-10-16', quota: 500000, requests: 1 },
    { name: '2026-10-17', quota: 1000000, requests: 2 },
  ],
  families: [{ name: 'Seedream', quota: 1500000, requests: 3 }],
  models: [{ name: 'seedream-5.0', quota: 1500000, requests: 3 }],
}
beforeEach(async () => {
  await i18n.changeLanguage('en')
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'project-owner', role: 1 })
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/token/projects') {
      return {
        data: {
          success: true,
          data: [
            { id: 10, name: 'Image product' },
            { id: 20, name: 'Video product' },
          ],
        },
      }
    }
    if (url.startsWith('/api/token/project-usage?')) {
      return { data: { success: true, data: report } }
    }
    throw new Error('Unexpected request')
  })
})
afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})
function showPanel() {
  return render(
    <I18nextProvider i18n={i18n}>
      <QueryClientProvider client={client}>
        <ProjectUsagePanel />
      </QueryClientProvider>
    </I18nextProvider>
  )
}
it('switches project and period together while showing totals, families and daily details', async () => {
  showPanel()
  expect(await screen.findByText('seedream-5.0')).toBeInTheDocument()
  expect(screen.getByText('Project total consumption')).toBeInTheDocument()
  expect(screen.getByText('Seedream')).toBeInTheDocument()
  expect(screen.getByText('2026-10-16')).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole('combobox', { name: 'Select project' })
  )
  await userEvent.click(
    await screen.findByRole('option', { name: 'Image product' })
  )
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      '/api/token/project-usage?days=30&project_id=10'
    )
  )
  await userEvent.click(screen.getByRole('button', { name: 'Last 7 days' }))
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      '/api/token/project-usage?days=7&project_id=10'
    )
  )
  expect(screen.getByRole('button', { name: 'Last 7 days' })).toHaveAttribute(
    'aria-pressed',
    'true'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Today' }))
  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith(
      '/api/token/project-usage?days=1&project_id=10'
    )
  )
})
it('clears previous project figures during a pending switch and shows an error instead of zero', async () => {
  showPanel()
  await screen.findByText('seedream-5.0')
  let rejectRequest: (error: Error) => void = () => {}
  vi.mocked(api.get).mockImplementation(async (url) => {
    if (url.includes('project_id=20')) {
      return new Promise((_resolve, reject) => {
        rejectRequest = reject
      })
    }
    return { data: { success: true, data: report } }
  })
  await userEvent.click(
    screen.getByRole('combobox', { name: 'Select project' })
  )
  await userEvent.click(
    await screen.findByRole('option', { name: 'Video product' })
  )
  expect(screen.queryByText('seedream-5.0')).not.toBeInTheDocument()
  rejectRequest(new Error('Unavailable'))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Failed to load usage'
  )
  expect(screen.queryByText('seedream-5.0')).not.toBeInTheDocument()
})
it('shows empty project details with Chinese translations', async () => {
  await i18n.changeLanguage('zh')
  vi.mocked(api.get).mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/token/projects'
          ? []
          : {
              ...report,
              total_quota: 0,
              period_quota: 0,
              requests: 0,
              families: [],
              models: [],
              daily: [{ name: '2026-10-17', quota: 0, requests: 0 }],
            },
    },
  }))
  showPanel()
  expect(
    await screen.findByText(i18n.t('Project total consumption'))
  ).toBeInTheDocument()
  expect(
    await screen.findByText(i18n.t('Daily consumption'))
  ).toBeInTheDocument()
  expect(await screen.findByText('2026-10-17')).toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: i18n.t('Last 30 days') })
  ).toHaveAttribute('aria-pressed', 'true')
  expect(i18n.t('Project total consumption')).not.toBe(
    'Project total consumption'
  )
})
