import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { ChannelCostConfig } from '@/features/channels/components/channel-cost-fields'
import { usePriceSources } from '@/features/channels/price-sources-api'
import { api } from '@/lib/api'
import { formatCNYAmount, formatUSDAmount } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'

type Rates = { input: number; output: number; cache_read: number }
type Cost = {
  dynamic?: boolean
  task_usage_billing?: boolean
  expression?: string
  charge_unit?: { unit: string; units: number; price: number }
  upstream_request_price?: number
  cny_request_price?: number
  upstream: Rates
  cny: Rates
  tier: string
  upstream_group_ratio: number
  synced_at: number
  source_url: string
  model: string
  tiers?: { label: string; max_context?: number; rates: Rates }[]
}
type ChannelCosts = {
  id: number
  name: string
  status: number
  source_name?: string
  config: ChannelCostConfig
  prices: { model: string; cost?: Cost; error?: string }[]
}
const queryKey = ['channel-cost-prices']

export function ChannelCostPrices(props: { selectSource?: boolean } = {}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const sources = usePriceSources()
  const [selectedChannel, setSelectedChannel] = useState<string>('')
  const query = useQuery({
    queryKey,
    queryFn: async () =>
      requireServerSuccess((await api.get('/api/channel/costs')).data)
        .data as ChannelCosts[],
    refetchInterval: 60_000,
  })
  const sync = useMutation({
    mutationFn: async (id: number) =>
      requireServerSuccess(
        (await api.post(`/api/channel/${id}/costs/sync`)).data
      ),
    onSuccess: async () => {
      await Promise.all([
        client.invalidateQueries({ queryKey }),
        client.invalidateQueries({ queryKey: ['pricing'] }),
      ])
    },
  })
  if (query.isPending) return <LoadingState />
  if (query.isError) {
    return (
      <ErrorState
        description={query.error.message}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return (
    <div className='space-y-5'>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Each channel keeps its own prices, even for identical model names. The final routed channel determines the CNY charge. Local group multiplier: 1.'
        )}
      </p>
      <p className='text-muted-foreground text-sm'>
        {t(
          'CNY prices below are already converted. Do not multiply them by the currency display exchange rate again.'
        )}
      </p>
      {props.selectSource && query.data.length > 0 && (
        <div className='space-y-2'>
          <label htmlFor='channel-price-source' className='text-sm font-medium'>
            {t('Channel')} / {t('Price source')}
          </label>
          <Select
            items={query.data.map((channel) => ({
              value: String(channel.id),
              label: `${channel.name} ← ${channel.source_name || (channel.config.source === 'happyapi' ? 'HappyAPI' : channel.config.source)} #${channel.id}`,
            }))}
            value={selectedChannel}
            onValueChange={(value) => {
              setSelectedChannel(value ?? '')
              sync.reset()
            }}
            disabled={sync.isPending}
          >
            <SelectTrigger id='channel-price-source' className='w-full'>
              <SelectValue placeholder={t('Select a channel price source')} />
            </SelectTrigger>
            <SelectContent>
              {query.data.map((channel) => (
                <SelectItem key={channel.id} value={String(channel.id)}>
                  {channel.name} ←{' '}
                  {channel.source_name ||
                    (channel.config.source === 'happyapi'
                      ? 'HappyAPI'
                      : channel.config.source)}{' '}
                  #{channel.id}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Only configured channel sources are available. Shared official presets cannot overwrite channel costs.'
            )}
          </p>
        </div>
      )}
      {sync.isError && (
        <p role='alert' className='text-destructive text-sm'>
          {sync.error.message}
        </p>
      )}
      {query.data.length === 0 && (
        <p className='text-muted-foreground py-8 text-center'>
          {t('Enable CNY cost billing in a channel to synchronize its prices.')}
        </p>
      )}
      {query.data
        .filter(
          (channel) =>
            !props.selectSource || String(channel.id) === selectedChannel
        )
        .map((channel) => (
          <section key={channel.id} className='space-y-3 rounded-lg border p-4'>
            <div className='flex flex-wrap items-center justify-between gap-3'>
              <div className='min-w-0 space-y-1'>
                <h3 className='font-medium break-all'>
                  {channel.name}{' '}
                  <span className='text-muted-foreground'>#{channel.id}</span>
                </h3>
                <p className='text-muted-foreground text-xs'>
                  {t(
                    sources.data?.sources?.find(
                      (source) => source.id === channel.config.source
                    )?.price_currency === 'USD'
                      ? 'Upstream USD received per CNY 1'
                      : 'Upstream credits received per CNY 1'
                  )}
                  :{' '}
                  {sources.data?.sources?.find(
                    (source) => source.id === channel.config.source
                  )?.price_currency === 'USD'
                    ? formatUSDAmount(channel.config.credits_per_cny, {
                        digitsLarge: 14,
                        digitsSmall: 14,
                        abbreviate: false,
                      })
                    : formatNumber(channel.config.credits_per_cny)}{' '}
                  · {t('Price source')}:{' '}
                  {channel.source_name ||
                    (channel.config.source === 'happyapi'
                      ? 'HappyAPI'
                      : channel.config.source)}{' '}
                  / {channel.config.upstream_group || '—'} ·{' '}
                  {channel.status === 1 ? t('Enabled') : t('Disabled')}
                </p>
              </div>
              <Button
                variant='outline'
                size='sm'
                disabled={sync.isPending}
                onClick={() => sync.mutate(channel.id)}
              >
                {sync.isPending && sync.variables === channel.id
                  ? t('Synchronizing...')
                  : t('Synchronize channel prices')}
              </Button>
            </div>
            <Table>
              <TableHeader>
                <TableRow>
                  {[
                    'Model',
                    'Billing unit',
                    'Upstream input / output / request',
                    'Upstream group multiplier',
                    'CNY input / 1M',
                    'CNY output / 1M',
                    'CNY cache read / 1M',
                    'CNY per request',
                    'Price source / status',
                  ].map((key) => (
                    <TableHead key={key}>{t(key)}</TableHead>
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {channel.prices.map(({ model, cost, error }) => (
                  <TableRow key={model}>
                    <TableCell className='max-w-80 break-all whitespace-normal'>
                      {model}
                      {cost?.charge_unit && (
                        <div className='text-muted-foreground text-xs'>
                          {formatCNYAmount(cost.charge_unit.price, {
                            digitsLarge: 8,
                            digitsSmall: 8,
                            abbreviate: false,
                          })}{' '}
                          × {formatNumber(cost.charge_unit.units)} /{' '}
                          {t(
                            cost.charge_unit.unit === 'second'
                              ? 'Second'
                              : 'Request'
                          )}
                        </div>
                      )}
                      {cost?.dynamic && (
                        <div className='mt-1 text-xs'>
                          <div className='text-muted-foreground'>
                            {t('CNY billing expression')}
                          </div>
                          <code className='block max-w-80 break-all whitespace-normal'>
                            {cost.expression}
                          </code>
                        </div>
                      )}
                      {cost && cost.model !== model && (
                        <div className='text-muted-foreground text-xs'>
                          → {cost.model}
                        </div>
                      )}
                    </TableCell>
                    {cost ? (
                      <>
                        <TableCell>
                          {cost.dynamic && t('Billing expression')}
                          {!cost.dynamic &&
                            t(
                              cost.cny_request_price != null
                                ? 'Per-request'
                                : 'Per-token'
                            )}
                        </TableCell>
                        <TableCell>
                          {cost.dynamic && '—'}
                          {!cost.dynamic &&
                            (cost.upstream_request_price != null ? (
                              cost.upstream_request_price.toLocaleString(
                                undefined,
                                { maximumFractionDigits: 8 }
                              )
                            ) : (
                              <>
                                {formatNumber(cost.upstream.input)} /{' '}
                                {formatNumber(cost.upstream.output)}
                              </>
                            ))}
                        </TableCell>
                        <TableCell>
                          {formatNumber(cost.upstream_group_ratio)}×
                        </TableCell>
                        {(['input', 'output', 'cache_read'] as const).map(
                          (kind) => (
                            <TableCell key={kind}>
                              {(cost.dynamic ||
                                cost.cny_request_price != null) &&
                                '—'}
                              {!cost.dynamic &&
                                cost.cny_request_price == null &&
                                (cost.tiers?.length
                                  ? cost.tiers.map((tier) => (
                                      <div
                                        key={`${tier.max_context ?? 'last'}:${tier.label}`}
                                        className='mb-2 text-xs'
                                      >
                                        <div className='text-muted-foreground'>
                                          {tier.max_context
                                            ? `≤ ${formatNumber(tier.max_context)}`
                                            : t(
                                                'Above previous context threshold'
                                              )}
                                        </div>
                                        <div>
                                          {formatCNYAmount(tier.rates[kind], {
                                            digitsLarge: 8,
                                            digitsSmall: 8,
                                            abbreviate: false,
                                          })}
                                        </div>
                                      </div>
                                    ))
                                  : formatCNYAmount(cost.cny[kind], {
                                      digitsLarge: 8,
                                      digitsSmall: 8,
                                      abbreviate: false,
                                    }))}
                            </TableCell>
                          )
                        )}
                        <TableCell>
                          {cost.cny_request_price != null
                            ? formatCNYAmount(cost.cny_request_price, {
                                digitsLarge: 4,
                                digitsSmall: 8,
                                abbreviate: false,
                              })
                            : '—'}
                        </TableCell>
                        <TableCell className='text-xs'>
                          <a
                            href={cost.source_url}
                            target='_blank'
                            rel='noreferrer'
                            className='underline'
                          >
                            {t('Source')}
                          </a>{' '}
                          ·{' '}
                          {t(
                            (
                              {
                                peak: 'Peak',
                                off_peak: 'Off-peak',
                                request: 'Per-request',
                                context: 'Context tiers',
                              } as Record<string, string>
                            )[cost.tier] ?? 'Standard'
                          )}
                          <div>
                            {new Date(cost.synced_at * 1000).toLocaleString()}
                          </div>
                        </TableCell>
                      </>
                    ) : (
                      <TableCell
                        colSpan={8}
                        className='text-destructive whitespace-normal'
                      >
                        {t(error || 'Channel prices are unavailable')}
                      </TableCell>
                    )}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </section>
        ))}
    </div>
  )
}
