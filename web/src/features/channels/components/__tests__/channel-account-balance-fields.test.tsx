import { render, screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { expect, test, vi } from 'vitest'

import { Form } from '@/components/ui/form'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformFormDataToCreatePayload,
  type ChannelFormValues,
} from '../../lib/channel-form'
import { ChannelAccountBalanceFields } from '../channel-account-balance-fields'

function Fields(props: { disabled?: boolean; save: (value: unknown) => void }) {
  const form = useForm<ChannelFormValues>({
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      type: 58,
      name: 'Own account',
      key: 'model-call-key',
      models: 'grok-4.2',
      group: ['default'],
      base_url: 'https://happyapi.org',
    },
  })
  return (
    <Form {...form}>
      <form
        onSubmit={form.handleSubmit((value) =>
          props.save(transformFormDataToCreatePayload(value))
        )}
      >
        <ChannelAccountBalanceFields
          form={form}
          disabled={Boolean(props.disabled)}
          isEditing
        />
        <button type='submit'>Save</button>
      </form>
    </Form>
  )
}
test('masks the account token and submits it separately without replacing the model key', async () => {
  const save = vi.fn()
  render(<Fields save={save} />)
  const token = screen.getByLabelText('Upstream account access token')
  expect(token).toHaveAttribute('type', 'password')
  expect(token).toHaveAttribute(
    'placeholder',
    'Leave empty to keep the existing key'
  )
  fireEvent.change(token, { target: { value: 'account-fixture' } })
  fireEvent.change(screen.getByLabelText('Upstream account user ID'), {
    target: { value: '42' },
  })
  await userEvent.setup().click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(save).toHaveBeenCalled())
  const channel = save.mock.calls[0][0].channel
  expect(channel.key).toBe('model-call-key')
  expect(JSON.parse(channel.setting)).toMatchObject({
    super_key: 'account-fixture',
    super_key_user_id: 42,
  })
})
test('blocks credential edits without sensitive write permission', () => {
  render(<Fields disabled save={vi.fn()} />)
  expect(screen.getByLabelText('Upstream account access token')).toBeDisabled()
  expect(screen.getByLabelText('Upstream account user ID')).toBeDisabled()
})
