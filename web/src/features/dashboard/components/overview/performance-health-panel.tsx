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
import { useQuery } from '@tanstack/react-query'
import { Gauge, HeartPulse, Timer } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { IconBadge, type IconBadgeTone } from '@/components/ui/icon-badge'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { getPerfMetricsSummary } from '@/features/performance-metrics/api'
import {
  formatLatency,
  formatThroughput,
  formatUptimePct,
  getSuccessRateDotClass,
  getSuccessRateTextClass,
} from '@/features/performance-metrics/lib/format'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatTimestamp } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import { getModelHealth, type ModelHealthRow } from '../../api'

const PERFORMANCE_WINDOW_HOURS = 24
const TOP_MODEL_LIMIT = 6

export function PerformanceHealthPanel() {
  const { t } = useTranslation()
  const metricsQuery = useQuery({
    queryKey: ['perf-metrics-summary', PERFORMANCE_WINDOW_HOURS],
    queryFn: async () =>
      requireServerSuccess(
        await getPerfMetricsSummary(PERFORMANCE_WINDOW_HOURS)
      ),
    staleTime: 60 * 1000,
    retry: false,
  })

  const models = useMemo(
    () => metricsQuery.data?.data.models ?? [],
    [metricsQuery.data]
  )

  const summary = metricsQuery.data?.data.summary

  const topModels = useMemo(() => models.slice(0, TOP_MODEL_LIMIT), [models])
  const loading = metricsQuery.isLoading
  const hasData = models.length > 0

  return (
    <section className='bg-card h-full overflow-hidden rounded-2xl border shadow-xs'>
      <div className='flex items-center gap-2 border-b px-4 py-3 sm:px-5'>
        <IconBadge tone='success' size='sm'>
          <HeartPulse />
        </IconBadge>
        <h3 className='text-sm font-semibold'>{t('Performance health')}</h3>
        <span className='text-muted-foreground ml-auto text-xs'>
          {t('Performance metrics for the last 24 hours')}
        </span>
      </div>

      <div className='space-y-3 p-4 sm:p-5'>
        <div className='grid grid-cols-3 gap-2'>
          <MetricCell
            icon={HeartPulse}
            label={t('Success rate')}
            value={formatUptimePct(summary?.success_rate ?? Number.NaN)}
            loading={loading}
            valueClassName={getSuccessRateTextClass(
              summary?.success_rate ?? Number.NaN
            )}
            tone='success'
          />
          <MetricCell
            icon={Timer}
            label={t('Average latency')}
            value={formatLatency(summary?.avg_latency_ms ?? 0)}
            loading={loading}
            tone='warning'
          />
          <MetricCell
            icon={Gauge}
            label={t('Throughput')}
            value={formatThroughput(summary?.avg_tps ?? 0)}
            loading={loading}
            tone='info'
          />
        </div>

        {loading ? (
          <div className='space-y-1'>
            {['success', 'latency', 'throughput'].map((key) => (
              <Skeleton key={key} className='h-5 w-full rounded' />
            ))}
          </div>
        ) : (
          hasData && (
            <div>
              <span className='text-muted-foreground mb-1 block text-[11px] font-medium'>
                {t('Top models by traffic')}
              </span>
              <div className='grid grid-cols-1 gap-x-4 sm:grid-cols-2'>
                {topModels.map((model) => (
                  <div
                    key={model.model_name}
                    className='flex items-center justify-between gap-2 rounded px-1.5 py-1'
                  >
                    <span className='min-w-0 flex-1 truncate font-mono text-[11px]'>
                      {model.model_name}
                    </span>
                    <span className='inline-flex shrink-0 items-center gap-1'>
                      <span
                        className={cn(
                          'size-1.5 rounded-full',
                          getSuccessRateDotClass(model.success_rate)
                        )}
                        aria-hidden='true'
                      />
                      <span
                        className={cn(
                          'font-mono text-[11px] font-semibold tabular-nums',
                          getSuccessRateTextClass(model.success_rate)
                        )}
                      >
                        {formatUptimePct(model.success_rate)}
                      </span>
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )
        )}
      </div>

      <ModelConnectivityTable />
    </section>
  )
}

function ModelConnectivityTable() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [search, setSearch] = useState('')
  const query = useQuery({
    queryKey: ['model-health'],
    queryFn: async () => requireServerSuccess(await getModelHealth()),
    refetchInterval: 60000,
    retry: false,
  })
  const rows = (query.data?.data || []).filter((row) =>
    `${row.model} ${row.channel_name} ${row.groups.map((g) => g.name).join(' ')}`
      .toLowerCase()
      .includes(search.toLowerCase())
  )
  const states = {
    unknown: t('Not checked'),
    unverified: t('No free model check'),
    healthy: t('Healthy'),
    unhealthy: t('Unhealthy'),
    interface_only: t('API reachable; generation unverified'),
    stale: t('Check expired'),
    disabled: t('Unavailable'),
  }
  const columns = [
    {
      id: 'channel',
      header: t('Channel'),
      cell: (r: ModelHealthRow) => r.channel_name,
    },
    {
      id: 'group',
      header: t('Group'),
      cell: (r: ModelHealthRow) => r.groups.map((g) => g.name).join(', '),
    },
    {
      id: 'model',
      header: t('Model ID'),
      cell: (r: ModelHealthRow) => r.model,
    },
    {
      id: 'rate',
      header: t('Probe success rate'),
      cell: (r: ModelHealthRow) =>
        r.success_rate == null
          ? '—'
          : `${formatNumber(r.success_rate * 100, locale)}%`,
    },
    {
      id: 'latency',
      header: t('Response (ms)'),
      cell: (r: ModelHealthRow) =>
        r.checked_at ? formatNumber(r.latency_ms, locale) : '—',
    },
    {
      id: 'state',
      header: t('Health'),
      cell: (r: ModelHealthRow) => states[r.state],
    },
    {
      id: 'checked',
      header: t('Last checked'),
      cell: (r: ModelHealthRow) =>
        r.checked_at ? formatTimestamp(r.checked_at) : '—',
    },
  ]
  return (
    <section
      aria-label={t('Model connectivity')}
      className='space-y-3 border-t p-4 sm:p-5'
    >
      <h4 className='text-sm font-semibold'>{t('Model connectivity')}</h4>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Chat is checked at most once every 30 minutes. Images and videos use free API discovery only. Rates describe recorded checks, not guaranteed generation availability.'
        )}
      </p>
      <Input
        aria-label={t('Filter model connectivity')}
        placeholder={t('Filter model connectivity')}
        value={search}
        onChange={(event) => setSearch(event.target.value)}
      />
      {query.isPending && <Skeleton className='h-24 w-full' />}
      {query.isError && (
        <p role='alert'>{t('Failed to load model connectivity')}</p>
      )}
      {!query.isPending && !query.isError && (
        <StaticDataTable
          className='max-h-96 overflow-y-auto'
          columns={columns}
          data={rows}
          getRowKey={(row) => `${row.channel_id}:${row.model}`}
          emptyContent={t('No matching models')}
        />
      )}
    </section>
  )
}

function MetricCell(props: {
  icon: React.ComponentType<{ className?: string }>
  label: string
  value: string
  loading: boolean
  valueClassName?: string
  tone: IconBadgeTone
}) {
  const Icon = props.icon
  return (
    <div className='bg-muted/40 rounded-xl px-3 py-2.5'>
      <div className='text-muted-foreground flex items-center gap-1.5 text-[11px] font-medium'>
        <IconBadge tone={props.tone} size='xs'>
          <Icon />
        </IconBadge>
        <span className='truncate'>{props.label}</span>
      </div>
      {props.loading ? (
        <Skeleton className='mt-1.5 h-5 w-16' />
      ) : (
        <div
          className={cn(
            'mt-1.5 font-mono text-sm font-semibold tabular-nums',
            props.valueClassName
          )}
        >
          {props.value}
        </div>
      )}
    </div>
  )
}
