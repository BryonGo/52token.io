import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import { useGroupLabelStore } from '@/stores/group-label-store'

import { useGroupLabels } from '../use-group-labels'

let client: QueryClient
afterEach(() => {
  cleanup()
  client?.clear()
  vi.restoreAllMocks()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useGroupLabelStore.setState({ labels: {} })
})
it('loads current group labels for an authenticated user whose session metadata is absent', async () => {
  useAuthStore.getState().auth.setUser({ id: 7, username: 'owner', role: 100 })
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { route_08: { name: 'New line name' } } },
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  renderHook(() => useGroupLabels(), {
    wrapper: ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    ),
  })
  await waitFor(() =>
    expect(useGroupLabelStore.getState().labels.route_08?.name).toBe(
      'New line name'
    )
  )
  expect(get).toHaveBeenCalledWith('/api/token/group-labels')
})

it('clears cached group names when the authenticated account is removed', async () => {
  useAuthStore.getState().auth.setUser({ id: 7, username: 'owner', role: 100 })
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { route_a: { name: 'Private admin label' } },
    },
  })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  renderHook(() => useGroupLabels(), {
    wrapper: ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    ),
  })
  await waitFor(() =>
    expect(useGroupLabelStore.getState().labels.route_a?.name).toBe(
      'Private admin label'
    )
  )
  act(() => useAuthStore.getState().auth.setUser(null))
  await waitFor(() => expect(useGroupLabelStore.getState().labels).toEqual({}))
  expect(get).toHaveBeenCalledTimes(1)
})
