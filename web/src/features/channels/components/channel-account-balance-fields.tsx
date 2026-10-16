import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { PasswordInput } from '@/components/password-input'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

import type { ChannelFormValues } from '../lib/channel-form'

export function ChannelAccountBalanceFields(props: {
  form: UseFormReturn<ChannelFormValues>
  disabled: boolean
  isEditing: boolean
}) {
  const { t } = useTranslation()
  return (
    <fieldset
      disabled={props.disabled}
      className='space-y-4 rounded-lg border p-4'
    >
      <legend className='px-1 text-sm font-medium'>
        {t('New API account balance')}
      </legend>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Configure the upstream account separately for this channel. An account access token is different from the model API key; administrator privileges are not required.'
        )}
      </p>
      <FormField
        control={props.form.control}
        name='super_key'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Upstream account access token')}</FormLabel>
            <FormControl>
              <PasswordInput
                {...field}
                disabled={props.disabled}
                autoComplete='off'
                maxLength={8192}
                className='font-mono'
                placeholder={
                  props.isEditing
                    ? t('Leave empty to keep the existing key')
                    : t('Account-level access token of the upstream gateway')
                }
              />
            </FormControl>
            <FormDescription>
              {t(
                'Used only for the account balance query. Each channel keeps its own credentials; saving does not change the model API key.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
      <FormField
        control={props.form.control}
        name='super_key_user_id'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Upstream account user ID')}</FormLabel>
            <FormControl>
              <Input
                {...field}
                disabled={props.disabled}
                type='number'
                min={0}
                step={1}
                value={field.value ?? ''}
                onChange={(event) =>
                  field.onChange(
                    event.target.value === ''
                      ? undefined
                      : Number(event.target.value)
                  )
                }
                placeholder={t(
                  'Optional when the upstream does not require it'
                )}
              />
            </FormControl>
            <FormDescription>
              {t(
                'Enter the user ID shown in that upstream account’s profile when New-Api-User is required.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
      <p className='text-muted-foreground text-xs'>
        {t(
          'For CNY cost billing, the balance is converted using this channel’s recharge ratio. Click the channel balance to refresh after saving.'
        )}
      </p>
    </fieldset>
  )
}
