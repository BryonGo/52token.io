import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useForm } from 'react-hook-form'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { Form } from '@/components/ui/form'
import { api } from '@/lib/api'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  type ChannelFormValues,
} from '../../lib/channel-form'
import { ChannelCostFields } from '../channel-cost-fields'

function Settings({
  disabled = false,
  onSave,
  type = 58,
  baseUrl = '',
}: {
  disabled?: boolean
  type?: number
  baseUrl?: string
  onSave: (value: string | undefined) => void
}) {
  const form = useForm<ChannelFormValues>({
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type,
      base_url: baseUrl,
      settings: '{"unrelated":"keep"}',
    },
  })
  return (
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <Form {...form}>
        <form onSubmit={form.handleSubmit((values) => onSave(values.settings))}>
          <ChannelCostFields form={form} disabled={disabled} />
          <button type='submit'>Save</button>
        </form>
      </Form>
    </QueryClientProvider>
  )
}
beforeEach(() => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        version: 'v1',
        sources: [{ id: 'happyapi', name: 'HappyAPI', enabled: true }],
      },
    },
  })
})
describe('channel cost settings', () => {
  test('enabling HappyAPI records five credits per yuan and preserves other settings', async () => {
    const save = vi.fn()
    render(<Settings onSave={save} />)
    fireEvent.click(
      screen.getByRole('switch', { name: 'Charge actual cost without markup' })
    )
    expect(
      screen.getByRole('spinbutton', {
        name: 'Upstream credits received per CNY 1',
      })
    ).toHaveValue(5)
    fireEvent.change(
      screen.getByRole('textbox', { name: 'Upstream token group' }),
      { target: { value: 'vip' } }
    )
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(save).toHaveBeenCalled())
    const saved = JSON.parse(save.mock.calls[0][0])
    expect(saved.unrelated).toBe('keep')
    expect(saved.cost_billing).toEqual({
      enabled: true,
      source: 'happyapi',
      credits_per_cny: 5,
      upstream_group: 'vip',
    })
  })
  test('users without sensitive write permission cannot enable cost billing', () => {
    render(<Settings disabled onSave={vi.fn()} />)
    expect(
      screen.getByRole('switch', { name: 'Charge actual cost without markup' })
    ).toHaveAttribute('aria-disabled', 'true')
  })
  test('rejects zero recharge ratios and automatic upstream groups', () => {
    const form = {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'Happy',
      type: 58,
      key: 'fixture',
      base_url: 'https://happyapi.org',
      models: 'DeepSeek-V3',
      group: ['default'],
    }
    for (const config of [
      {
        enabled: true,
        source: 'happyapi',
        credits_per_cny: 0,
        upstream_group: 'default',
      },
      {
        enabled: true,
        source: 'happyapi',
        credits_per_cny: 5,
        upstream_group: 'auto',
      },
    ]) {
      const parsed = channelFormSchema.safeParse({
        ...form,
        settings: JSON.stringify({ cost_billing: config }),
      })
      expect(parsed.success).toBe(false)
      if (!parsed.success) {
        expect(
          parsed.error.issues.some((issue) => issue.path[0] === 'settings')
        ).toBe(true)
      }
    }
  })
})

test('Sub2API defaults to its own origin-bound source and one credit per yuan', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        version: 'v1',
        sources: [
          {
            id: 'pojia',
            name: 'POJIA AI',
            format: 'sub2api',
            enabled: true,
            channel_origin: 'https://api.pojia.ai',
          },
        ],
      },
    },
  })
  const save = vi.fn()
  render(<Settings onSave={save} type={59} baseUrl='https://api.pojia.ai' />)
  await screen.findByRole('switch', {
    name: 'Charge actual cost without markup',
  })
  fireEvent.click(
    screen.getByRole('switch', { name: 'Charge actual cost without markup' })
  )
  await screen.findByText('POJIA AI')
  expect(
    screen.getByRole('spinbutton', {
      name: 'Upstream credits received per CNY 1',
    })
  ).toHaveValue(1)
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Upstream token group' }),
    { target: { value: 'POJIAAI-国模破甲' } }
  )
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(save).toHaveBeenCalled())
  expect(JSON.parse(save.mock.calls[0][0]).cost_billing).toEqual({
    enabled: true,
    source: 'pojia',
    credits_per_cny: 1,
    upstream_group: 'POJIAAI-国模破甲',
  })
})
