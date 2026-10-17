import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { useGroupLabelStore } from '@/stores/group-label-store'

import { ApiKeyGroupCombobox } from '../api-key-group-combobox'
import { AutoGroupOrderEditor } from '../auto-group-order-editor'

afterEach(() => {
  cleanup()
  useGroupLabelStore.setState({ labels: {} })
})
it('renames a selected key group without changing the fixed value submitted by the selector', async () => {
  const select = vi.fn()
  useGroupLabelStore.setState({ labels: { route_a: { name: 'New line' } } })
  render(
    <ApiKeyGroupCombobox
      value='route_a'
      options={[{ value: 'route_a', label: 'Old line', ratio: 0.08 }]}
      onValueChange={select}
    />
  )
  expect(screen.getByRole('combobox')).toHaveTextContent('New line')
  expect(screen.getByRole('combobox')).not.toHaveTextContent('Old line')
  await userEvent.click(screen.getByRole('combobox'))
  await userEvent.click(await screen.findByRole('option', { name: /New line/ }))
  await waitFor(() => expect(select).toHaveBeenCalledWith('route_a'))
})

it('uses current names for Auto group controls while keeping fixed IDs in reorder and removal updates', async () => {
  useGroupLabelStore.setState({
    labels: { route_a: { name: 'Renamed A' }, route_b: { name: 'Renamed B' } },
  })
  const change = vi.fn()
  render(
    <AutoGroupOrderEditor
      value={['route_a', 'route_b']}
      mode='custom'
      options={[
        { value: 'route_a', label: 'Old A', ratio: 1 },
        { value: 'route_b', label: 'Old B', ratio: 1 },
      ]}
      globalOptions={[]}
      maxCount={5}
      onChange={change}
    />
  )
  expect(screen.getByText('Renamed A')).toBeVisible()
  await userEvent.click(
    screen.getByRole('button', { name: 'Move Renamed A down' })
  )
  expect(change).toHaveBeenLastCalledWith({
    groups: ['route_b', 'route_a'],
    mode: 'custom',
  })
  await userEvent.click(
    screen.getByRole('button', { name: 'Remove Renamed A' })
  )
  expect(change).toHaveBeenLastCalledWith({
    groups: ['route_b'],
    mode: 'custom',
  })
})
