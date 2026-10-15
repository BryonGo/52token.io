/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import type { QueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { updateChannelBalance } from '../../api'
import { handleBatchUpdateBalance } from '../channel-actions'

vi.mock('../../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api')>()),
  updateChannelBalance: vi.fn(),
}))
vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

const updateChannelBalanceMock = vi.mocked(updateChannelBalance)
const toastSuccess = vi.mocked(toast.success)
const toastError = vi.mocked(toast.error)

function queryClientStub() {
  return {
    invalidateQueries: vi.fn(),
  } as unknown as QueryClient & { invalidateQueries: ReturnType<typeof vi.fn> }
}

describe('handleBatchUpdateBalance', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  test('reports the updated count and refreshes the channel list', async () => {
    updateChannelBalanceMock.mockResolvedValue({ success: true, balance: 1 })
    const queryClient = queryClientStub()

    await handleBatchUpdateBalance([1, 2], queryClient)

    expect(updateChannelBalanceMock).toHaveBeenCalledWith(1)
    expect(updateChannelBalanceMock).toHaveBeenCalledWith(2)
    expect(queryClient.invalidateQueries).toHaveBeenCalledTimes(1)
    expect(toastSuccess).toHaveBeenCalledTimes(1)
    expect(String(toastSuccess.mock.calls[0][0])).toContain(
      'channel balance(s) updated'
    )
    expect(toastError).not.toHaveBeenCalled()
  })

  test('keeps the upstream reason for the channels that failed', async () => {
    updateChannelBalanceMock.mockResolvedValueOnce({
      success: true,
      balance: 1,
    })
    updateChannelBalanceMock.mockResolvedValueOnce({
      success: false,
      message: 'upstream key has unlimited quota',
    })

    await handleBatchUpdateBalance([1, 2], queryClientStub())

    expect(toastSuccess).toHaveBeenCalledTimes(1)
    expect(toastError).toHaveBeenCalledTimes(1)
    expect(String(toastError.mock.calls[0][0])).toContain(
      'channel balance(s) failed to update'
    )
    expect(toastError.mock.calls[0][1]).toEqual({
      description: 'upstream key has unlimited quota',
    })
  })

  test('asks for a selection before querying any channel', async () => {
    const queryClient = queryClientStub()

    await handleBatchUpdateBalance([], queryClient)

    expect(updateChannelBalanceMock).not.toHaveBeenCalled()
    expect(toastError).toHaveBeenCalledWith('No channels selected')
    expect(queryClient.invalidateQueries).not.toHaveBeenCalled()
  })
})
