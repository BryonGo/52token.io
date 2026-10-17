import { useQuery } from '@tanstack/react-query'
import { CalendarDays, Wallet } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { getKeyProjects } from '@/features/keys/api'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { getProjectUsage, type ProjectUsageAmount } from '../../api'
import { PanelWrapper } from '../ui/panel-wrapper'
import { StatCard } from '../ui/stat-card'

export function ProjectUsagePanel() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const userId = useAuthStore((s) => s.auth.user?.id)
  useSystemConfigStore((s) => s.config.currency)
  const [project, setProject] = useState('all')
  const [days, setDays] = useState(30)
  const projects = useQuery({
    queryKey: ['key-projects'],
    queryFn: async () => requireServerSuccess(await getKeyProjects()),
  })
  const usage = useQuery({
    queryKey: ['project-usage', userId, project, days],
    queryFn: async () =>
      requireServerSuccess(
        await getProjectUsage(
          project === 'all' ? undefined : Number(project),
          days
        )
      ),
    refetchInterval: 60000,
  })
  const report = usage.data?.data
  const projectList = Array.isArray(projects.data?.data)
    ? projects.data.data
    : []
  const columns = [
    {
      id: 'name',
      header: t('Model family'),
      cell: (r: ProjectUsageAmount) =>
        r.name === 'Unclassified' ? t('Unclassified') : r.name,
    },
    {
      id: 'quota',
      header: t('Charged amount'),
      cell: (r: ProjectUsageAmount) => formatQuota(r.quota),
    },
    {
      id: 'requests',
      header: t('Requests'),
      cell: (r: ProjectUsageAmount) => formatNumber(r.requests, locale),
    },
  ]
  return (
    <section aria-label={t('Project usage')} className='space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <h2 className='font-semibold'>{t('Project usage')}</h2>
        <div className='flex flex-wrap items-center gap-2'>
          <Combobox
            aria-label={t('Select project')}
            className='w-56'
            openOnFocus
            value={project}
            onValueChange={(value) => setProject(value || 'all')}
            disabled={projects.isPending || projects.isError}
            options={[
              { value: 'all', label: t('All projects') },
              { value: '0', label: t('Unassigned') },
              ...projectList.map((p) => ({
                value: String(p.id),
                label: p.name,
              })),
            ]}
          />
          <div className='flex flex-wrap gap-1' aria-label={t('Usage period')}>
            {[
              { days: 1, label: t('Today') },
              { days: 7, label: t('Last 7 days') },
              { days: 30, label: t('Last 30 days') },
            ].map((period) => (
              <Button
                key={period.days}
                size='sm'
                variant={days === period.days ? 'secondary' : 'ghost'}
                aria-pressed={days === period.days}
                onClick={() => setDays(period.days)}
              >
                {period.label}
              </Button>
            ))}
          </div>
          <Button
            variant='outline'
            size='sm'
            disabled={usage.isFetching}
            onClick={() => {
              void usage.refetch()
              void projects.refetch()
            }}
          >
            {t('Refresh')}
          </Button>
        </div>
      </div>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Cumulative charges include retained keys. Daily and model breakdowns use retained consumption logs, in Beijing time. Moving a key moves its report history. These are local charges, not an upstream invoice.'
        )}
      </p>
      {projects.isError && <p role='alert'>{t('Failed to load projects')}</p>}
      {usage.isError && <p role='alert'>{t('Failed to load usage')}</p>}
      <div className='grid gap-4 md:grid-cols-2'>
        <StatCard
          title={t('Project total consumption')}
          value={report ? formatQuota(report.total_quota) : '—'}
          description={t('Cumulative charges for current project keys')}
          icon={Wallet}
          loading={usage.isPending}
          error={usage.isError}
        />
        <StatCard
          title={t('Consumption in selected period')}
          value={report ? formatQuota(report.period_quota) : '—'}
          description={`${t('Requests')}: ${report ? formatNumber(report.requests, locale) : '—'}`}
          icon={CalendarDays}
          loading={usage.isPending}
          error={usage.isError}
          sparkline={report?.daily.map((day) => day.quota)}
        />
      </div>
      {report && !usage.isError && (
        <>
          <div className='grid gap-4 xl:grid-cols-2'>
            <PanelWrapper title={t('Consumption by model family')}>
              <StaticDataTable
                data={report.families}
                getRowKey={(r) => r.name}
                columns={columns}
                emptyContent={t('No usage records')}
              />
            </PanelWrapper>
            <PanelWrapper
              title={t('Daily consumption')}
              description={t('Beijing time (UTC+8)')}
            >
              <div className='max-h-80 overflow-auto'>
                <StaticDataTable
                  data={[...report.daily].reverse()}
                  getRowKey={(r) => r.name}
                  columns={[
                    {
                      id: 'date',
                      header: t('Date'),
                      cell: (r: ProjectUsageAmount) => r.name,
                    },
                    ...columns.slice(1),
                  ]}
                  emptyContent={t('No usage records')}
                />
              </div>
            </PanelWrapper>
          </div>
          <PanelWrapper title={t('Consumption by model')}>
            <div className='max-h-80 overflow-auto'>
              <StaticDataTable
                data={report.models}
                getRowKey={(r) => r.name}
                columns={[
                  {
                    id: 'model',
                    header: t('Model'),
                    cell: (r: ProjectUsageAmount) => r.name,
                  },
                  ...columns.slice(1),
                ]}
                emptyContent={t('No usage records')}
              />
            </div>
          </PanelWrapper>
        </>
      )}
    </section>
  )
}
