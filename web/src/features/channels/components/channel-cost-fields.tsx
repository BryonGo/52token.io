import { useWatch, type UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import type { ChannelFormValues } from '../lib/channel-form'
import { usePriceSources } from '../price-sources-api'

export type ChannelCostConfig = {
  enabled: boolean
  source: string
  credits_per_cny: number
  upstream_group?: string
}
export function ChannelCostFields({
  form,
  disabled,
}: {
  form: UseFormReturn<ChannelFormValues>
  disabled: boolean
}) {
  const { t } = useTranslation()
  const sources = usePriceSources()
  const raw = useWatch({ control: form.control, name: 'settings' }) || '{}'
  const channelType = useWatch({ control: form.control, name: 'type' })
  const baseUrl = useWatch({ control: form.control, name: 'base_url' })
  const matchingSource = sources.data?.sources.find((source) => {
    if (!source.enabled || !baseUrl) return false
    try {
      return new URL(source.channel_origin).origin === new URL(baseUrl).origin
    } catch {
      return false
    }
  })
  let settings: Record<string, unknown>
  try {
    settings = JSON.parse(raw)
  } catch {
    return null
  }
  let defaultSource = channelType === 43 ? 'deepseek' : 'happyapi'
  if (channelType === 59) defaultSource = matchingSource?.id || ''
  let defaultGroup: string | undefined = 'default'
  if (channelType === 43) defaultGroup = undefined
  if (channelType === 59) defaultGroup = ''
  let value: ChannelCostConfig =
    (settings.cost_billing as ChannelCostConfig) ?? {
      enabled: false,
      source: defaultSource,
      credits_per_cny: channelType === 43 || channelType === 59 ? 1 : 5,
      upstream_group: defaultGroup,
    }
  if (channelType === 59 && !value.source && matchingSource) {
    value = { ...value, source: matchingSource.id }
  }
  const update = (patch: Partial<ChannelCostConfig>) =>
    form.setValue(
      'settings',
      JSON.stringify({ ...settings, cost_billing: { ...value, ...patch } }),
      { shouldDirty: true, shouldValidate: true }
    )
  return (
    <fieldset disabled={disabled} className='space-y-4 rounded-lg border p-4'>
      <legend className='px-1 text-sm font-medium'>
        {t('Channel CNY cost billing')}
      </legend>
      <FormField
        control={form.control}
        name='settings'
        render={() => (
          <FormItem>
            <div className='flex items-center justify-between gap-3'>
              <FormLabel htmlFor='channel-cost-enabled'>
                {t('Charge actual cost without markup')}
              </FormLabel>
              <FormControl>
                <Switch
                  id='channel-cost-enabled'
                  checked={value.enabled}
                  onCheckedChange={(enabled) => update({ enabled })}
                  disabled={disabled}
                />
              </FormControl>
            </div>
            <FormDescription>
              {t(
                'CNY cost = upstream price × upstream group multiplier ÷ credits received per yuan. Local group multiplier is fixed at 1.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
      {value.enabled && (
        <div className='grid gap-4 sm:grid-cols-2'>
          <div className='space-y-2'>
            <label
              htmlFor='channel-cost-source'
              className='text-sm font-medium'
            >
              {t('Upstream price source')}
            </label>
            <Select
              value={value.source}
              onValueChange={(source) => {
                if (source) {
                  let upstreamGroup = value.upstream_group || 'default'
                  if (source === 'deepseek') upstreamGroup = ''
                  const selectedSource = sources.data?.sources.find(
                    (item) => item.id === source
                  )
                  if (selectedSource?.format === 'sub2api') {
                    upstreamGroup = value.upstream_group || ''
                  }
                  update({
                    source,
                    upstream_group:
                      source === 'deepseek' ? undefined : upstreamGroup,
                  })
                }
              }}
            >
              <SelectTrigger id='channel-cost-source' className='w-full'>
                <SelectValue>
                  {
                    sources.data?.sources.find(
                      (source) => source.id === value.source
                    )?.name
                  }
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {(sources.data?.sources ?? [])
                  .filter((source) => source.enabled)
                  .map((source) => (
                    <SelectItem key={source.id} value={source.id}>
                      {source.name}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
            {sources.isError && (
              <p className='text-destructive text-xs'>
                {sources.error.message}
              </p>
            )}
            <p className='text-muted-foreground text-xs'>
              {t(
                'Maintain sources in Billing settings → Model pricing → Price sources.'
              )}
            </p>
          </div>
          <div className='space-y-2'>
            <label
              htmlFor='channel-cost-credits'
              className='text-sm font-medium'
            >
              {t('Upstream credits received per CNY 1')}
            </label>
            <Input
              id='channel-cost-credits'
              type='number'
              min='0.000001'
              max='1000000'
              step='any'
              value={value.credits_per_cny ?? ''}
              onChange={(event) =>
                update({ credits_per_cny: Number(event.target.value) })
              }
            />
            <p className='text-muted-foreground text-xs'>
              {t('DeepSeek CNY: 1. HappyAPI: CNY 0.2 buys 1 credit, enter 5.')}
            </p>
          </div>
          {value.source !== 'deepseek' && (
            <div className='space-y-2 sm:col-span-2'>
              <label
                htmlFor='channel-cost-group'
                className='text-sm font-medium'
              >
                {t('Upstream token group')}
              </label>
              <Input
                id='channel-cost-group'
                value={value.upstream_group ?? ''}
                onChange={(event) =>
                  update({ upstream_group: event.target.value.trim() })
                }
                placeholder='default / vip'
              />
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Enter the group assigned to this upstream token. Its public multiplier is synchronized; account-specific discounts and automatic groups are not supported.'
                )}
              </p>
            </div>
          )}
          <p className='text-muted-foreground text-xs sm:col-span-2'>
            {t(
              'Prices sync after saving and every hour. Missing or expired prices block requests. Review channel costs in Model pricing.'
            )}
          </p>
        </div>
      )}
    </fieldset>
  )
}
