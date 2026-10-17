import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { ChannelCostPrices } from '../channel-cost-prices'

const clients: QueryClient[] = []
afterEach(() => {
  vi.restoreAllMocks()
  clients.forEach((client) => client.clear())
  clients.length = 0
})
function setup(props: { selectSource?: boolean } = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ChannelCostPrices selectSource={props.selectSource} />
    </QueryClientProvider>
  )
}
const price = {
  model: 'DeepSeek-V3',
  cost: {
    model: 'DeepSeek-V3',
    upstream: { input: 1, output: 4, cache_read: 0.25 },
    cny: { input: 0.2, output: 0.8, cache_read: 0.05 },
    upstream_group_ratio: 1,
    tier: 'standard',
    synced_at: 1792112400,
    source_url: 'https://happyapi.org/api/pricing',
  },
}
describe('channel CNY costs', () => {
  test('shows channel and source names while synchronizing only the selected channel ID', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: [127, 134].map((id) => ({
          id,
          name: id === 127 ? 'HappyAPI · DeepSeek' : 'POJIA · DeepSeek',
          source_name:
            id === 127 ? 'HappyAPI · 上游价' : 'POJIA · DeepSeek 参考价',
          status: 1,
          config: {
            source: id === 127 ? 'happyapi' : 'pojia-deepseek',
            credits_per_cny: id === 127 ? 5 : 1,
            upstream_group: 'default',
          },
          prices: [price],
        })),
      },
    })
    const post = vi
      .spyOn(api, 'post')
      .mockResolvedValue({ data: { success: true } })
    setup({ selectSource: true })
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('combobox', { name: 'Channel / Price source' })
    )
    expect(
      await screen.findByRole('option', {
        name: 'HappyAPI · DeepSeek ← HappyAPI · 上游价 #127',
      })
    ).toBeVisible()
    await user.click(
      screen.getByRole('option', {
        name: 'POJIA · DeepSeek ← POJIA · DeepSeek 参考价 #134',
      })
    )
    expect(await screen.findAllByRole('table')).toHaveLength(1)
    expect(screen.getByText('¥0.2')).toBeVisible()
    await user.click(
      screen.getByRole('button', { name: 'Synchronize channel prices' })
    )
    await waitFor(() =>
      expect(post).toHaveBeenCalledExactlyOnceWith(
        '/api/channel/134/costs/sync'
      )
    )
  })
  test('keeps identically named models in separate channels and displays converted yuan once', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: [
          {
            id: 1,
            name: 'Happy',
            status: 1,
            config: {
              source: 'happyapi',
              credits_per_cny: 5,
              upstream_group: 'default',
            },
            prices: [price],
          },
          {
            id: 2,
            name: 'Second channel',
            status: 1,
            config: {
              source: 'happyapi',
              credits_per_cny: 5,
              upstream_group: 'vip',
            },
            prices: [
              {
                ...price,
                cost: {
                  ...price.cost,
                  upstream: { ...price.cost.upstream, input: 1.2, output: 5 },
                  cny: { input: 0.24, output: 1, cache_read: 0.06 },
                },
              },
            ],
          },
        ],
      },
    })
    setup()
    const tables = await screen.findAllByRole('table')
    expect(tables).toHaveLength(2)
    expect(within(tables[0]).getByText('¥0.2')).toBeInTheDocument()
    expect(within(tables[0]).getByText('¥0.8')).toBeInTheDocument()
    expect(within(tables[1]).getByText('¥0.24')).toBeInTheDocument()
    expect(within(tables[1]).getByText('¥1')).toBeInTheDocument()
  })
  test('displays failed synchronization and leaves the retry button available', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: [
          {
            id: 1,
            name: 'Happy',
            status: 1,
            config: {
              source: 'happyapi',
              credits_per_cny: 5,
              upstream_group: 'default',
            },
            prices: [
              { model: 'missing', error: 'Channel prices are unavailable' },
            ],
          },
        ],
      },
    })
    vi.spyOn(api, 'post').mockRejectedValue(new Error('Upstream unavailable'))
    setup()
    fireEvent.click(
      await screen.findByRole('button', { name: 'Synchronize channel prices' })
    )
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Upstream unavailable'
    )
    expect(
      screen.getByRole('button', { name: 'Synchronize channel prices' })
    ).toBeEnabled()
  })
  test('explains how to enable pricing when no channels are configured', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: [] },
    })
    setup()
    expect(
      await screen.findByText(
        'Enable CNY cost billing in a channel to synchronize its prices.'
      )
    ).toBeInTheDocument()
  })
})

test('displays upstream and CNY request prices without misleading token prices', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: [
        {
          id: 126,
          name: 'Happy request channel',
          status: 1,
          config: {
            source: 'happyapi',
            credits_per_cny: 5,
            upstream_group: 'default',
          },
          prices: [
            {
              model: 'grok-4-fast-ci',
              cost: {
                ...price.cost,
                upstream_request_price: 0.025,
                cny_request_price: 0.005,
                tier: 'request',
                expression: 'tier("request", fixed(0.005))',
                upstream: { input: 0, output: 0, cache_read: 0 },
                cny: { input: 0, output: 0, cache_read: 0 },
              },
            },
          ],
        },
      ],
    },
  })
  setup()
  const row = (await screen.findByText('grok-4-fast-ci')).closest('tr')
  expect(row).not.toBeNull()
  expect(row).toHaveTextContent('0.025')
  expect(row).toHaveTextContent('¥0.005')
  expect(row).not.toHaveTextContent('¥0.00 ')
  expect(
    within(row as HTMLTableRowElement).getByText('Per-request')
  ).toBeVisible()
})

test('USD recharge conversion is labelled in dollars and retains its fractional precision', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/channel/price-sources'
          ? {
              version: 'v1',
              sources: [{ id: 'deepseek-usd', price_currency: 'USD' }],
            }
          : [
              {
                id: 133,
                name: 'DeepSeek official',
                status: 1,
                source_name: 'DeepSeek USD',
                config: { source: 'deepseek-usd', credits_per_cny: 1 / 6.8 },
                prices: [],
              },
            ],
    },
  }))
  setup()
  expect(
    await screen.findByText(
      /Upstream USD received per CNY 1: \$0\.14705882352941/
    )
  ).toBeVisible()
})

test('per-second costs show their CNY expression and never masquerade as free token prices', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: [
        {
          id: 133,
          name: 'NewToken video',
          status: 1,
          config: { source: 'newtoken-video', credits_per_cny: 1 },
          prices: [
            {
              model: 'video-ultra-720p',
              cost: {
                dynamic: true,
                task_usage_billing: true,
                expression: 'tier("second", u("seconds") * 0.5)',
                charge_unit: { unit: 'second', units: 1, price: 0.5 },
                cny: { input: 0, output: 0, cache_read: 0 },
                upstream: { input: 0, output: 0, cache_read: 0 },
                tier: 'expression',
                upstream_group_ratio: 0.5,
                source_url: 'https://newtoken.club/api/pricing',
              },
            },
          ],
        },
      ],
    },
  })
  setup()
  expect(await screen.findByText('CNY billing expression')).toBeVisible()
  expect(screen.getByText('tier("second", u("seconds") * 0.5)')).toBeVisible()
  expect(screen.getByText('¥0.5 × 1 / Second')).toBeVisible()
  expect(screen.queryByText('Per-token')).not.toBeInTheDocument()
  expect(screen.queryByText('¥0')).not.toBeInTheDocument()
})
