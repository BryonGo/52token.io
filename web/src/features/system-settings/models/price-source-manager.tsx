import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { JsonCodeEditor } from '@/components/json-code-editor'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  EMPTY_SOURCE_FIELDS,
  SOURCE_FIELD_LABELS,
  priceSourcesKey,
  usePriceSources,
  type PriceSource,
  type SourceField,
} from '@/features/channels/price-sources-api'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

const referenceSchema = z.object({
  group_ratio: z.number().positive().max(1e6),
  models: z
    .record(z.string().min(1).max(256), z.string().min(1).max(256))
    .refine((value) => Object.keys(value).length > 0),
  schedule: z.enum(['deepseek', 'off_peak']),
  local_group_billing: z.boolean(),
})
const sourceSchema = z.object({
  id: z.string().regex(/^[a-z][a-z0-9_-]{0,63}$/),
  name: z.string().min(1).max(120),
  enabled: z.boolean(),
  format: z.enum(['json', 'newapi', 'sub2api', 'deepseek_html']),
  url: z.url(),
  channel_origin: z.url(),
  status_url: z.union([z.literal(''), z.url()]),
  unit_tokens: z.number().positive().max(1e9),
  input_scale: z.number().positive().max(1e9),
  output_ratio: z.boolean(),
  cache_ratio: z.boolean(),
  reference_json: z.string().refine((value) => {
    if (!value.trim()) return true
    try {
      return referenceSchema.safeParse(JSON.parse(value)).success
    } catch {
      return false
    }
  }, 'Invalid confirmed reference pricing'),
  fields: z.object({
    description: z.string(),
    vendor: z.string(),
    vendor_id: z.string(),
    icon: z.string(),
    tags: z.string(),
    endpoints: z.string(),
    vendors: z.string(),
    vendor_key: z.string(),
    vendor_name: z.string(),
    vendor_icon: z.string(),
    request_price: z.string(),
    quota_type: z.string(),
    items: z.string(),
    model: z.string(),
    input: z.string(),
    output: z.string(),
    cache_read: z.string(),
    cache_write: z.string(),
    cache_write_1h: z.string(),
    billing_mode: z.string(),
    expression: z.string(),
    groups: z.string(),
    group_ratios: z.string(),
    quota_unit: z.string(),
    success: z.string(),
  }),
})
type PriceSourceForm = z.infer<typeof sourceSchema>
function sourceFromForm(values: PriceSourceForm): PriceSource {
  const { reference_json, ...source } = values
  return {
    ...source,
    reference: reference_json.trim()
      ? referenceSchema.parse(JSON.parse(reference_json))
      : undefined,
  }
}

const newApiFields = {
  ...EMPTY_SOURCE_FIELDS,
  description: 'description',
  vendor_id: 'vendor_id',
  icon: 'icon',
  tags: 'tags',
  endpoints: 'supported_endpoint_types',
  vendors: 'vendors',
  vendor_key: 'id',
  vendor_name: 'name',
  vendor_icon: 'icon',
  request_price: 'model_price',
  quota_type: 'quota_type',
  items: 'data',
  model: 'model_name',
  input: 'model_ratio',
  output: 'completion_ratio',
  cache_read: 'cache_ratio',
  cache_write: 'create_cache_ratio',
  billing_mode: 'billing_mode',
  expression: 'billing_expr',
  groups: 'enable_groups',
  group_ratios: 'group_ratio',
  quota_unit: 'data.quota_per_unit',
  success: 'success',
}
const sub2apiFields = {
  ...EMPTY_SOURCE_FIELDS,
  items: 'data.groups',
  groups: 'models',
  group_ratios: 'rate_multiplier',
  model: 'name',
  input: 'pricing.input_price',
  output: 'pricing.output_price',
  cache_read: 'pricing.cache_read_price',
  cache_write: 'pricing.cache_write_price',
  cache_write_1h: 'pricing.cache_write_1h_price',
  request_price: 'pricing.per_request_price',
  billing_mode: 'pricing.billing_mode',
}
function newSource(): PriceSourceForm {
  return {
    id: '',
    name: '',
    enabled: true,
    format: 'json',
    url: '',
    channel_origin: '',
    status_url: '',
    unit_tokens: 1_000_000,
    input_scale: 1,
    output_ratio: false,
    cache_ratio: false,
    reference_json: '',
    fields: {
      ...EMPTY_SOURCE_FIELDS,
      items: 'data',
      model: 'id',
      input: 'pricing.input',
      output: 'pricing.output',
    },
  }
}
type Preview = {
  models: number
  unsupported: number
  group_ratio: number
  prices: Record<string, unknown>
  errors: Record<string, string>
}

export function PriceSourceManager() {
  const { t } = useTranslation()
  const client = useQueryClient()
  const query = usePriceSources()
  const [editing, setEditing] = useState<string | null>(null)
  const [open, setOpen] = useState(false)
  const [group, setGroup] = useState('default')
  const form = useForm<PriceSourceForm>({
    resolver: zodResolver(sourceSchema),
    defaultValues: newSource(),
  })
  const format = form.watch('format')
  const save = useMutation({
    mutationFn: async (source: PriceSource) => {
      if (
        editing === null &&
        query.data?.sources.some((item) => item.id === source.id)
      ) {
        throw new Error(t('Price source ID already exists'))
      }
      return requireServerSuccess(
        (
          await api.put('/api/channel/price-sources', {
            source,
            expected_version: query.data?.version,
          })
        ).data
      )
    },
    onSuccess: async () => {
      await Promise.all([
        client.invalidateQueries({ queryKey: priceSourcesKey }),
        client.invalidateQueries({ queryKey: ['channel-cost-prices'] }),
        client.invalidateQueries({ queryKey: ['pricing'] }),
      ])
      setOpen(false)
    },
  })
  const preview = useMutation({
    mutationFn: async (source: PriceSource) =>
      requireServerSuccess(
        (
          await api.post('/api/channel/price-sources/preview', {
            source,
            group,
          })
        ).data
      ).data as Preview,
  })
  const edit = (source?: PriceSource) => {
    let defaults = newSource()
    if (source) {
      let fields = EMPTY_SOURCE_FIELDS
      if (source.format === 'newapi') fields = newApiFields
      if (source.format === 'sub2api') fields = sub2apiFields
      defaults = {
        ...defaults,
        ...source,
        reference_json: source.reference
          ? JSON.stringify(source.reference, null, 2)
          : '',
        fields: { ...fields, ...source.fields },
      }
    }
    form.reset(defaults)
    setEditing(source?.id ?? null)
    save.reset()
    preview.reset()
    setOpen(true)
  }
  const applyGatewayTemplate = (format: 'newapi' | 'sub2api') => {
    const values = form.getValues()
    let origin: string
    try {
      const url = new URL(values.channel_origin.trim() || values.url.trim())
      if (
        !['http:', 'https:'].includes(url.protocol) ||
        url.username ||
        url.password
      ) {
        throw new Error('Invalid origin')
      }
      origin = url.origin
    } catch {
      form.setError('channel_origin', {
        message: t('Enter the upstream website URL first'),
      })
      return
    }
    form.reset({
      ...values,
      format,
      channel_origin: origin,
      url: `${origin}${format === 'sub2api' ? '/api/v1/model-plaza' : '/api/pricing'}`,
      status_url: format === 'sub2api' ? '' : `${origin}/api/status`,
      unit_tokens: format === 'sub2api' ? 1 : 1_000_000,
      input_scale: 1,
      output_ratio: format === 'newapi',
      cache_ratio: format === 'newapi',
      reference_json: '',
      fields: { ...(format === 'sub2api' ? sub2apiFields : newApiFields) },
    })
    preview.reset()
  }
  if (query.isPending) return <LoadingState />
  if (query.isError) {
    return (
      <ErrorState
        description={query.error.message}
        onRetry={() => void query.refetch()}
      />
    )
  }
  const busy = save.isPending || preview.isPending
  return (
    <div className='space-y-4'>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Configure a public GET pricing endpoint and map its JSON fields. Bind the source in channel CNY cost settings; each channel keeps its own purchase ratio.'
        )}
      </p>
      <Button onClick={() => edit()}>{t('Add price source')}</Button>
      <Table>
        <TableHeader>
          <TableRow>
            {['Name', 'Price source', 'Status', 'Actions'].map((label) => (
              <TableHead key={label}>{t(label)}</TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {query.data.sources.map((source) => (
            <TableRow key={source.id}>
              <TableCell>
                <div>{source.name}</div>
                <code className='text-xs'>{source.id}</code>
              </TableCell>
              <TableCell className='max-w-sm text-xs break-all'>
                {source.url}
              </TableCell>
              <TableCell>
                {t(source.enabled ? 'Enabled' : 'Disabled')}
              </TableCell>
              <TableCell>
                <Button
                  variant='outline'
                  size='sm'
                  onClick={() => edit(source)}
                  aria-label={t('Edit price source {{name}}', {
                    name: source.name,
                  })}
                >
                  {t('Edit')}
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!busy) setOpen(value)
        }}
        title={t(editing === null ? 'Add price source' : 'Edit price source')}
        contentClassName='sm:max-w-3xl'
        contentHeight='min(75vh, 800px)'
        footer={
          <>
            <Button
              variant='outline'
              disabled={busy}
              onClick={() => setOpen(false)}
            >
              {t('Cancel')}
            </Button>
            <Button
              variant='outline'
              disabled={busy}
              onClick={() =>
                void form.handleSubmit((source) =>
                  preview.mutate(sourceFromForm(source))
                )()
              }
            >
              {t('Test field mappings')}
            </Button>
            <Button type='submit' form='price-source-form' disabled={busy}>
              {t('Save')}
            </Button>
          </>
        }
      >
        <Form {...form}>
          <form
            id='price-source-form'
            className='space-y-4'
            onSubmit={form.handleSubmit((source) =>
              save.mutate(sourceFromForm(source))
            )}
          >
            <div className='space-y-2'>
              <Button
                type='button'
                variant='outline'
                disabled={busy}
                onClick={() => applyGatewayTemplate('newapi')}
              >
                {t('Use New API template')}
              </Button>
              <Button
                type='button'
                variant='outline'
                disabled={busy}
                onClick={() => applyGatewayTemplate('sub2api')}
              >
                {t('Use Sub2API template')}
              </Button>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Enter the upstream website URL, then apply the New API template to fill pricing and status endpoints and standard field mappings.'
                )}
              </p>
            </div>
            <div className='grid gap-4 sm:grid-cols-2'>
              {(
                ['id', 'name', 'url', 'channel_origin', 'status_url'] as const
              ).map((name) => (
                <FormField
                  key={name}
                  control={form.control}
                  name={name}
                  rules={{ required: name !== 'status_url' }}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>
                        {t(
                          {
                            id: 'Price source ID',
                            name: 'Name',
                            url: 'Pricing endpoint URL',
                            channel_origin: 'Bound channel origin',
                            status_url: 'Status endpoint URL (optional)',
                          }[name]
                        )}
                      </FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          disabled={busy || (name === 'id' && editing !== null)}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              ))}
              <FormField
                control={form.control}
                name='format'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Response format')}</FormLabel>
                    <Select
                      value={field.value}
                      onValueChange={(value) => {
                        if (
                          value === 'json' ||
                          value === 'newapi' ||
                          value === 'sub2api' ||
                          value === 'deepseek_html'
                        ) {
                          field.onChange(value)
                          if (value !== 'deepseek_html') {
                            form.setValue('reference_json', '')
                          }
                          if (value === 'newapi') {
                            form.setValue('fields', newApiFields)
                            form.setValue('output_ratio', true)
                            form.setValue('cache_ratio', true)
                            form.setValue(
                              'status_url',
                              `${form.getValues('channel_origin').replace(/\/$/, '')}/api/status`
                            )
                          } else if (value === 'sub2api') {
                            form.setValue('fields', { ...sub2apiFields })
                            form.setValue('unit_tokens', 1)
                            form.setValue('input_scale', 1)
                            form.setValue('output_ratio', false)
                            form.setValue('cache_ratio', false)
                            form.setValue('status_url', '')
                          }
                        }
                      }}
                    >
                      <FormControl>
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent>
                        <SelectItem value='json'>
                          {t('JSON field mapping')}
                        </SelectItem>
                        <SelectItem value='newapi'>New API</SelectItem>
                        <SelectItem value='sub2api'>Sub2API</SelectItem>
                        <SelectItem value='deepseek_html'>
                          {t('DeepSeek official HTML')}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                    <FormMessage />
                  </FormItem>
                )}
              />
              {(['unit_tokens', 'input_scale'] as const).map((name) => (
                <FormField
                  key={name}
                  control={form.control}
                  name={name}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>
                        {t(
                          name === 'unit_tokens'
                            ? 'Tokens per source price unit'
                            : 'Numeric price scale'
                        )}
                      </FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min='0.000000001'
                          step='any'
                          {...field}
                          onChange={(event) =>
                            field.onChange(Number(event.target.value))
                          }
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              ))}
              {(['enabled', 'output_ratio', 'cache_ratio'] as const).map(
                (name) => (
                  <FormField
                    key={name}
                    control={form.control}
                    name={name}
                    render={({ field }) => (
                      <FormItem className='flex items-center justify-between gap-3'>
                        <FormLabel>
                          {t(
                            {
                              enabled: 'Enabled',
                              output_ratio:
                                'Output field is an input-price multiplier',
                              cache_ratio:
                                'Cache fields are input-price multipliers',
                            }[name]
                          )}
                        </FormLabel>
                        <FormControl>
                          <Switch
                            checked={field.value}
                            onCheckedChange={field.onChange}
                            disabled={
                              format === 'sub2api' && name !== 'enabled'
                            }
                          />
                        </FormControl>
                      </FormItem>
                    )}
                  />
                )
              )}
            </div>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Token expressions use upstream units per million tokens; fixed expressions and request prices use upstream units per request. Request prices are automatically converted to fixed expressions.'
              )}
            </p>
            {format === 'deepseek_html' && (
              <FormField
                control={form.control}
                name='reference_json'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>
                      {t('Confirmed reference pricing (optional JSON)')}
                    </FormLabel>
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Only use a reference after confirming the upstream agreement. Map exact upstream model names in models, set group_ratio, and choose deepseek or off_peak for schedule. Sub2API verifies the key multiplier. Empty keeps official direct pricing.'
                      )}
                    </p>
                    <FormControl>
                      <JsonCodeEditor
                        value={field.value}
                        onChange={field.onChange}
                        onBlur={field.onBlur}
                        name={field.name}
                        textareaRef={field.ref}
                        disabled={busy}
                        ariaLabel={t(
                          'Confirmed reference pricing (optional JSON)'
                        )}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            )}
            {format !== 'deepseek_html' && (
              <>
                {format === 'sub2api' && (
                  <p className='text-muted-foreground text-xs'>
                    {t(
                      'Sub2API requires the upstream model plaza to be enabled. Select the exact group name. Prices come from channel pricing, not official reference prices; the channel key verifies its effective multiplier. Balance queries use the model key without an account access token.'
                    )}
                  </p>
                )}
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Use dot paths such as data and pricing.input. Leave optional paths empty. Numeric fields use the configured unit; billing expressions use upstream units per million tokens.'
                  )}
                </p>
                <div className='grid gap-4 sm:grid-cols-2'>
                  {(
                    Object.entries(SOURCE_FIELD_LABELS) as [
                      SourceField,
                      string,
                    ][]
                  ).map(([key, label]) => (
                    <FormField
                      key={key}
                      control={form.control}
                      name={`fields.${key}`}
                      render={({ field }) => (
                        <FormItem>
                          <FormLabel>
                            {t(
                              format === 'sub2api'
                                ? (
                                    {
                                      items: 'Group list path',
                                      groups: 'Model list path within group',
                                      group_ratios: 'Group multiplier field',
                                    } as Partial<Record<SourceField, string>>
                                  )[key] || label
                                : label
                            )}
                          </FormLabel>
                          <FormControl>
                            <Input {...field} />
                          </FormControl>
                          <FormMessage />
                        </FormItem>
                      )}
                    />
                  ))}
                </div>
              </>
            )}
            <label className='block space-y-2'>
              <span className='text-sm'>
                {t('Upstream group for mapping test')}
              </span>
              <Input
                value={group}
                onChange={(event) => setGroup(event.target.value)}
              />
            </label>
            {(save.isError || preview.isError) && (
              <p role='alert' className='text-destructive text-sm'>
                {save.error?.message ?? preview.error?.message}
              </p>
            )}
            {preview.data && (
              <div className='space-y-2'>
                <p>
                  {t(
                    'Matched models: {{count}}; unsupported: {{unsupported}}',
                    {
                      count: preview.data.models,
                      unsupported: preview.data.unsupported,
                    }
                  )}
                </p>
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'Mapping preview shows upstream units per million tokens before channel recharge conversion. At most 20 models are shown.'
                  )}
                </p>
                <pre className='max-h-72 overflow-auto rounded border p-3 text-xs'>
                  {JSON.stringify(preview.data, null, 2)}
                </pre>
              </div>
            )}
          </form>
        </Form>
      </Dialog>
    </div>
  )
}
