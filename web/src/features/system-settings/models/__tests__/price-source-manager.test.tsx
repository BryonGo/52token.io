import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { PriceSourceManager } from '../price-source-manager'

const clients: QueryClient[] = []
afterEach(() => {
  vi.restoreAllMocks()
  clients.forEach((client) => client.clear())
  clients.length = 0
})
function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <PriceSourceManager />
    </QueryClientProvider>
  )
}
async function fillSource() {
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Add price source' })
  )
  for (const [label, value] of [
    ['Price source ID', 'custom'],
    ['Name', 'Custom upstream'],
    ['Pricing endpoint URL', 'https://upstream.example/prices'],
    ['Bound channel origin', 'https://upstream.example'],
    ['Model list path', 'result.models'],
    ['Input price field', 'cost.input'],
    ['Output price field', 'cost.output'],
  ]) {
    fireEvent.change(screen.getByRole('textbox', { name: label }), {
      target: { value },
    })
  }
  return user
}
test('previews new field mappings without saving, then saves with the registry version', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { sources: [], version: 'v1' } },
  })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        models: 1,
        unsupported: 0,
        prices: { m: { input: 2 } },
        errors: {},
      },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  setup()
  const user = await fillSource()
  await user.click(screen.getByRole('button', { name: 'Test field mappings' }))
  expect(
    await screen.findByText('Matched models: 1; unsupported: 0')
  ).toBeVisible()
  expect(put).not.toHaveBeenCalled()
  expect(post).toHaveBeenCalledWith(
    '/api/channel/price-sources/preview',
    expect.objectContaining({
      source: expect.objectContaining({
        id: 'custom',
        fields: expect.objectContaining({
          items: 'result.models',
          input: 'cost.input',
        }),
      }),
    })
  )
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/channel/price-sources',
      expect.objectContaining({
        expected_version: 'v1',
        source: expect.objectContaining({
          id: 'custom',
          channel_origin: 'https://upstream.example',
        }),
      })
    )
  )
})
test('keeps an unsuccessful source save open with its field mappings intact', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { sources: [], version: 'v1' } },
  })
  vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: false,
      message: 'Price sources changed; reload before saving',
    },
  })
  setup()
  const user = await fillSource()
  await user.click(screen.getByRole('button', { name: 'Save' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Price sources changed; reload before saving'
  )
  expect(
    screen.getByRole('textbox', { name: 'Input price field' })
  ).toHaveValue('cost.input')
})

test('applies a reusable New API template for an upstream origin without saving', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { sources: [], version: 'v1' } },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  setup()
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Add price source' })
  )
  await user.click(screen.getByRole('button', { name: 'Use New API template' }))
  expect(
    await screen.findByText('Enter the upstream website URL first')
  ).toBeVisible()
  for (const [label, value] of [
    ['Price source ID', 'aizhongzhuan'],
    ['Name', '爱中转'],
    ['Bound channel origin', 'https://aizhongzhuan.com/pricing'],
  ]) {
    fireEvent.change(screen.getByRole('textbox', { name: label }), {
      target: { value },
    })
  }
  await user.click(screen.getByRole('button', { name: 'Use New API template' }))
  expect(put).not.toHaveBeenCalled()
  expect(
    screen.getByRole('textbox', { name: 'Pricing endpoint URL' })
  ).toHaveValue('https://aizhongzhuan.com/api/pricing')
  expect(
    screen.getByRole('textbox', { name: 'Status endpoint URL (optional)' })
  ).toHaveValue('https://aizhongzhuan.com/api/status')
  expect(
    screen.getByRole('textbox', { name: 'Bound channel origin' })
  ).toHaveValue('https://aizhongzhuan.com')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/channel/price-sources',
      expect.objectContaining({
        source: expect.objectContaining({
          id: 'aizhongzhuan',
          name: '爱中转',
          format: 'newapi',
          input_scale: 1,
          unit_tokens: 1000000,
          output_ratio: true,
          cache_ratio: true,
          fields: expect.objectContaining({
            items: 'data',
            model: 'model_name',
            input: 'model_ratio',
            output: 'completion_ratio',
            request_price: 'model_price',
            quota_type: 'quota_type',
            group_ratios: 'group_ratio',
            quota_unit: 'data.quota_per_unit',
          }),
        }),
      })
    )
  )
})

test('Sub2API template uses group-based per-token channel pricing and clears New API status mapping', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { sources: [], version: 'v1' } },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  setup()
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Add price source' })
  )
  for (const [label, value] of [
    ['Price source ID', 'pojia'],
    ['Name', 'POJIA AI'],
    ['Bound channel origin', 'https://api.pojia.ai'],
    ['Status endpoint URL (optional)', 'https://api.pojia.ai/api/status'],
  ]) {
    fireEvent.change(screen.getByRole('textbox', { name: label }), {
      target: { value },
    })
  }
  await user.click(screen.getByRole('button', { name: 'Use Sub2API template' }))
  expect(put).not.toHaveBeenCalled()
  expect(
    screen.getByRole('textbox', { name: 'Pricing endpoint URL' })
  ).toHaveValue('https://api.pojia.ai/api/v1/model-plaza')
  expect(
    screen.getByRole('textbox', { name: 'Status endpoint URL (optional)' })
  ).toHaveValue('')
  expect(screen.getByRole('textbox', { name: 'Group list path' })).toHaveValue(
    'data.groups'
  )
  expect(
    screen.getByRole('textbox', { name: 'Model list path within group' })
  ).toHaveValue('models')
  expect(
    screen.getByRole('spinbutton', { name: 'Tokens per source price unit' })
  ).toHaveValue(1)
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/channel/price-sources',
      expect.objectContaining({
        source: expect.objectContaining({
          id: 'pojia',
          format: 'sub2api',
          unit_tokens: 1,
          status_url: '',
          output_ratio: false,
          cache_ratio: false,
          fields: expect.objectContaining({
            items: 'data.groups',
            model: 'name',
            input: 'pricing.input_price',
            output: 'pricing.output_price',
            groups: 'models',
            group_ratios: 'rate_multiplier',
          }),
        }),
      })
    )
  )
})

test('editing a confirmed reference preserves exact model scope, schedule and local group billing', async () => {
  const reference = {
    group_ratio: 0.08,
    models: { 'deepseek-v4-pro': 'deepseek-v4-pro' },
    schedule: 'deepseek',
    local_group_billing: true,
  }
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        version: 'ref-v1',
        sources: [
          {
            id: 'pojia-deepseek',
            name: 'POJIA DeepSeek',
            enabled: true,
            format: 'deepseek_html',
            url: 'https://api-docs.deepseek.com/zh-cn/quick_start/pricing/',
            channel_origin: 'https://api.pojia.ai',
            unit_tokens: 1_000_000,
            input_scale: 1,
            output_ratio: false,
            cache_ratio: false,
            fields: {},
            reference,
          },
        ],
      },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  setup()
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', {
      name: 'Edit price source POJIA DeepSeek',
    })
  )
  expect(
    screen.getByRole('textbox', {
      name: 'Confirmed reference pricing (optional JSON)',
    })
  ).toHaveValue(JSON.stringify(reference, null, 2))
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/channel/price-sources',
      expect.objectContaining({
        expected_version: 'ref-v1',
        source: expect.objectContaining({ reference }),
      })
    )
  )
})
