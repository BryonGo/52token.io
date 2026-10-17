import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { PerformanceHealthPanel } from '../performance-health-panel'

let client: QueryClient
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})
afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
})

function mount(failed = false) {
  vi.spyOn(api, 'get').mockImplementation(async (path) => {
    if (path === '/api/perf-metrics/summary') {
      return { data: { success: true, data: { models: [], summary: null } } }
    }
    if (failed) throw new Error('Unavailable')
    expect(path).toBe('/api/channel/model-health')
    return {
      data: {
        success: true,
        data: Array.from({ length: 8 }, (_, index) => ({
          channel_id: index + 1,
          channel_name: `Vendor ${index}`,
          model: 'shared-image-model',
          groups: [{ id: `g${index}`, name: `Group ${index}` }],
          state: index === 0 ? 'unknown' : 'interface_only',
          routed: true,
          checked_at: index === 0 ? 0 : 1700000000,
          latency_ms: 123,
          success_rate: index === 0 ? null : 0.75,
          checks: index === 0 ? 0 : 4,
          method: 'directory',
        })),
      },
    }
  })
  return render(
    <QueryClientProvider client={client}>
      <PerformanceHealthPanel />
    </QueryClientProvider>
  )
}

it('shows all channel-specific models, distinguishes free API checks and leaves unknown results blank', async () => {
  const post = vi.spyOn(api, 'post')
  mount()
  expect(await screen.findByText('Vendor 7')).toBeVisible()
  expect(screen.getAllByText('shared-image-model')).toHaveLength(8)
  expect(
    screen.getAllByText('API reachable; generation unverified')
  ).toHaveLength(7)
  const unknown = screen.getByRole('row', { name: /Vendor 0/ })
  expect(within(unknown).getByText('Not checked')).toBeVisible()
  expect(within(unknown).queryByText('0%')).not.toBeInTheDocument()
  await userEvent.type(
    screen.getByRole('textbox', { name: 'Filter model connectivity' }),
    'Vendor 7'
  )
  expect(screen.getAllByText('shared-image-model')).toHaveLength(1)
  expect(screen.queryByText('Vendor 0')).not.toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
})

it('shows a failed fetch without claiming any model is healthy', async () => {
  mount(true)
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Failed to load model connectivity'
  )
  expect(
    screen.queryByText('API reachable; generation unverified')
  ).not.toBeInTheDocument()
})
