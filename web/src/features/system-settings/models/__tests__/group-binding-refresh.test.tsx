import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { ApiKeyGroupCell } from '@/features/keys/components/api-key-group-cell'
import { ApiKeyGroupCombobox } from '@/features/keys/components/api-key-group-combobox'
import { ModelConnections } from '@/features/models/components/model-connections'
import { useUpdateOption } from '@/features/system-settings/hooks/use-update-option'
import { getUserGroups, api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useGroupLabelStore } from '@/stores/group-label-store'

import { GroupManager } from '../group-manager'

let client: QueryClient

afterEach(() => {
  cleanup()
  client?.clear()
  vi.restoreAllMocks()
  useGroupLabelStore.setState({ labels: {} })
})
function BoundKey(props: { onSelect: (value: string) => void }) {
  const groups = useQuery({
    queryKey: ['user-groups'],
    queryFn: async () => requireServerSuccess(await getUserGroups()),
  })
  const info = groups.data?.data?.route_a
  return (
    <div>
      <div data-testid='key-selector'>
        <ApiKeyGroupCombobox
          value='route_a'
          options={
            info
              ? [
                  {
                    value: 'route_a',
                    label: info.name || 'Old line',
                    ratio: info.ratio,
                  },
                ]
              : []
          }
          onValueChange={props.onSelect}
        />
      </div>
      <div data-testid='key-row'>
        <ApiKeyGroupCell
          group='route_a'
          crossGroupRetry={false}
          shouldReduceMotion
          ratio={info?.ratio}
        />
      </div>
    </div>
  )
}
it('renaming and changing a multiplier refreshes model and key views while retaining the fixed binding', async () => {
  const group = {
    id: 'route_a',
    name: 'Old line',
    ratio: 0.08,
    defined: true,
    selectable: true,
    description: '',
    channels: [{ id: 134, name: 'POJIA' }],
    token_count: 1,
    user_count: 0,
  }
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/group/managed') {
      return {
        data: {
          success: true,
          data: { version: 'v1', groups: [{ ...group }] },
        },
      }
    }
    if (url === '/api/user/self/groups') {
      return {
        data: {
          success: true,
          data: { route_a: { name: group.name, ratio: group.ratio, desc: '' } },
        },
      }
    }
    throw new Error('Unexpected request')
  })
  const put = vi.spyOn(api, 'put').mockImplementation(async (_url, body) => {
    const updated = body as typeof group
    group.name = updated.name
    group.ratio = updated.ratio
    return { data: { success: true, data: { id: 'route_a' } } }
  })
  const select = vi.fn()
  render(
    <QueryClientProvider client={client}>
      <GroupManager />
      <BoundKey onSelect={select} />
      <div data-testid='model-connections'>
        <ModelConnections
          model={{
            id: 1,
            model_name: 'deepseek-v4-pro',
            status: 1,
            sync_official: 1,
            created_time: 0,
            updated_time: 0,
            name_rule: 0,
            enable_groups: ['route_a'],
            bound_channels: [{ name: 'POJIA', type: 59 }],
          }}
        />
      </div>
    </QueryClientProvider>
  )
  await waitFor(() =>
    expect(
      within(screen.getByTestId('key-row')).getByText('0.08x')
    ).toBeVisible()
  )
  await userEvent.click(await screen.findByRole('button', { name: 'Edit' }))
  await userEvent.clear(screen.getByLabelText('Group name'))
  await userEvent.type(screen.getByLabelText('Group name'), 'Renamed line')
  await userEvent.clear(screen.getByLabelText('Ratio'))
  await userEvent.type(screen.getByLabelText('Ratio'), '0.04')
  await userEvent.click(screen.getByRole('button', { name: /^Save$/ }))
  await waitFor(() =>
    expect(
      within(screen.getByTestId('model-connections')).getByText('Renamed line')
    ).toBeVisible()
  )
  await waitFor(() =>
    expect(
      within(screen.getByTestId('key-row')).getByText('0.04x')
    ).toBeVisible()
  )
  expect(
    within(screen.getByTestId('key-selector')).getByRole('combobox')
  ).toHaveTextContent('Renamed line')
  await userEvent.click(
    within(screen.getByTestId('key-selector')).getByRole('combobox')
  )
  await userEvent.click(
    await screen.findByRole('option', { name: /Renamed line/ })
  )
  expect(select).toHaveBeenCalledWith('route_a')
  expect(put).toHaveBeenCalledWith(
    '/api/group/managed',
    expect.objectContaining({
      id: 'route_a',
      name: 'Renamed line',
      ratio: 0.04,
    })
  )
})
function AdvancedMultiplierSave() {
  const save = useUpdateOption()
  return (
    <Button
      onClick={() =>
        save.mutate({ key: 'GroupRatio', value: '{"route_a":0.5}' })
      }
    >
      Save advanced multiplier
    </Button>
  )
}
it('saving an advanced multiplier refreshes an existing key table without reopening it', async () => {
  let ratio = 1
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { route_a: { name: 'Line', ratio, desc: '' } },
    },
  })
  vi.mocked(api.get).mockImplementation(async () => ({
    data: {
      success: true,
      data: { route_a: { name: 'Line', ratio, desc: '' } },
    },
  }))
  vi.spyOn(api, 'put').mockImplementation(async () => {
    ratio = 0.5
    return { data: { success: true } }
  })
  render(
    <QueryClientProvider client={client}>
      <AdvancedMultiplierSave />
      <BoundKey onSelect={() => {}} />
    </QueryClientProvider>
  )
  await screen.findAllByText('1x')
  await userEvent.click(
    screen.getByRole('button', { name: 'Save advanced multiplier' })
  )
  await waitFor(() =>
    expect(
      within(screen.getByTestId('key-row')).getByText('0.5x')
    ).toBeVisible()
  )
})

it('shows a separate ID column, locks the ID and permits editing the display name', async () => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        version: 'v1',
        groups: [
          {
            id: 'g1',
            name: 'POJIA',
            ratio: 0.08,
            defined: true,
            selectable: true,
            description: '',
            channels: [],
            token_count: 0,
            user_count: 0,
          },
        ],
      },
    },
  })
  render(
    <QueryClientProvider client={client}>
      <GroupManager />
    </QueryClientProvider>
  )
  expect(await screen.findByRole('columnheader', { name: 'ID' })).toBeVisible()
  expect(screen.getAllByRole('columnheader')[0]).toHaveTextContent(/^ID$/)
  await userEvent.click(await screen.findByRole('button', { name: 'Edit' }))
  expect(screen.getByLabelText('Fixed group ID')).toHaveAttribute('readonly')
  expect(screen.getByLabelText('Group name')).not.toHaveAttribute('readonly')
})
it('confirms deletion of an unused group and refreshes the list', async () => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  let groups = [
    {
      id: 'g2',
      name: 'Unused',
      ratio: 1,
      defined: true,
      selectable: true,
      description: '',
      channels: [],
      token_count: 0,
      user_count: 0,
    },
  ]
  vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: { success: true, data: { version: 'v1', groups } },
  }))
  const remove = vi.spyOn(api, 'delete').mockImplementation(async () => {
    groups = []
    return { data: { success: true } }
  })
  render(
    <QueryClientProvider client={client}>
      <GroupManager />
    </QueryClientProvider>
  )
  await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
  expect(remove).not.toHaveBeenCalled()
  await userEvent.click(
    screen.getByRole('button', { name: 'Confirm deletion' })
  )
  await waitFor(() =>
    expect(screen.queryByText('Unused')).not.toBeInTheDocument()
  )
  expect(remove).toHaveBeenCalledWith('/api/group/managed', {
    data: { id: 'g2', version: 'v1' },
  })
})
