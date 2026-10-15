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
import { toast } from 'sonner'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { handleServerError } from '@/lib/handle-server-error'

import { testChannel } from '../../api'
import { handleTestChannel } from '../channel-actions'

vi.mock('../../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api')>()),
  testChannel: vi.fn(),
}))
vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))
vi.mock('@/lib/handle-server-error', () => ({
  handleServerError: vi.fn(),
}))

const testChannelMock = vi.mocked(testChannel)
const toastSuccess = vi.mocked(toast.success)
const handleServerErrorMock = vi.mocked(handleServerError)

describe('handleTestChannel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  test('reports the models the connection test parked', async () => {
    testChannelMock.mockResolvedValue({
      success: true,
      time: 1.5,
      models: { usable: 59, disabled: 3 },
    })

    await handleTestChannel(126, { channelName: 'happyapi-x1' })

    expect(toastSuccess).toHaveBeenCalledTimes(1)
    const description = String(toastSuccess.mock.calls[0][1]?.description ?? '')
    expect(description).toContain('model(s) removed from routing')
  })

  test('keeps the response time only when no model was parked', async () => {
    testChannelMock.mockResolvedValue({
      success: true,
      time: 1.5,
      models: { usable: 62, disabled: 0 },
    })

    await handleTestChannel(126, { channelName: 'happyapi-x1' })

    const description = String(toastSuccess.mock.calls[0][1]?.description ?? '')
    expect(description).toContain('Response time')
    expect(description).not.toContain('model(s) removed from routing')
  })

  test('reports parked models next to the failure reason', async () => {
    testChannelMock.mockResolvedValue({
      success: false,
      message: 'no available channel for model',
      error_code: 'bad_response_status_code',
      models: { usable: 0, disabled: 4 },
    })

    await handleTestChannel(126, { channelName: 'happyapi-x1' })

    expect(handleServerErrorMock).toHaveBeenCalledTimes(1)
    const options = handleServerErrorMock.mock.calls[0][2]
    expect(String(options?.description ?? '')).toContain(
      'no available channel for model'
    )
    expect(String(options?.description ?? '')).toContain(
      'model(s) removed from routing'
    )
  })
})
