import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { MetadataSourcesDialog } from '../components/dialogs/metadata-sources-dialog'

const clients: QueryClient[] = []
afterEach(() => {
  vi.restoreAllMocks()
  clients.forEach((client) => client.clear())
  clients.length = 0
})
function setup(onSaved = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <MetadataSourcesDialog onClose={vi.fn()} onSaved={onSaved} />
    </QueryClientProvider>
  )
}
it('saves a reusable family rule with the loaded version and keeps price routing out of the request', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        version: 'v1',
        config: {
          channel_fallback: true,
          aliases: [
            {
              match: 'prefix',
              name: 'grok-',
              metadata_model: '',
              vendor: 'xAI',
            },
          ],
        },
      },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const onSaved = vi.fn()
  setup(onSaved)
  expect(
    await screen.findByRole('textbox', { name: 'Alias vendor 1' })
  ).toHaveValue('xAI')
  expect(
    screen.getByRole('switch', {
      name: 'Supplement from bound channel sources',
    })
  ).toBeChecked()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Add metadata alias' }))
  fireEvent.change(screen.getByRole('textbox', { name: 'Alias name 2' }), {
    target: { value: 'internal-opus' },
  })
  await user.click(screen.getByRole('button', { name: 'Save metadata rules' }))
  expect(put).not.toHaveBeenCalled()
  fireEvent.change(screen.getByRole('textbox', { name: 'Alias target 2' }), {
    target: { value: 'claude-opus-4-7' },
  })
  await user.click(screen.getByRole('button', { name: 'Save metadata rules' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/models/metadata-sources', {
      expected_version: 'v1',
      config: {
        channel_fallback: true,
        aliases: [
          { match: 'prefix', name: 'grok-', metadata_model: '', vendor: 'xAI' },
          {
            match: 'exact',
            name: 'internal-opus',
            metadata_model: 'claude-opus-4-7',
            vendor: '',
          },
        ],
      },
    })
  )
  await waitFor(() => expect(onSaved).toHaveBeenCalled())
})
it('retains draft rules when the server rejects a stale version', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        version: 'stale',
        config: { channel_fallback: true, aliases: [] },
      },
    },
  })
  vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: false,
      message: 'Metadata sources changed; reload before saving',
    },
  })
  setup()
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Add metadata alias' })
  )
  fireEvent.change(screen.getByRole('textbox', { name: 'Alias name 1' }), {
    target: { value: 'grok-' },
  })
  fireEvent.change(screen.getByRole('textbox', { name: 'Alias vendor 1' }), {
    target: { value: 'xAI' },
  })
  await user.click(screen.getByRole('button', { name: 'Save metadata rules' }))
  await waitFor(() => expect(api.put).toHaveBeenCalled())
  expect(screen.getByRole('textbox', { name: 'Alias vendor 1' })).toHaveValue(
    'xAI'
  )
})
