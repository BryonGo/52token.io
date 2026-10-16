import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import { useFieldArray, useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
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
import { handleServerError } from '@/lib/handle-server-error'

import {
  getMetadataSources,
  metadataSourcesKey,
  saveMetadataSources,
  type MetadataSourcesConfig,
} from '../../metadata-sources-api'

const schema = z.object({
  channel_fallback: z.boolean(),
  aliases: z
    .array(
      z
        .object({
          match: z.enum(['exact', 'prefix']),
          name: z.string().trim().min(1).max(200),
          metadata_model: z.string().trim().max(200),
          vendor: z.string().trim().max(200),
        })
        .superRefine((value, context) => {
          if (!value.metadata_model && !value.vendor) {
            context.addIssue({
              code: 'custom',
              path: ['vendor'],
              message: 'Choose a metadata model or vendor',
            })
          }
        })
    )
    .max(200),
})

export function MetadataSourcesDialog(props: {
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const query = useQuery({
    queryKey: metadataSourcesKey,
    queryFn: getMetadataSources,
  })
  const form = useForm<MetadataSourcesConfig>({
    resolver: zodResolver(schema),
    defaultValues: { channel_fallback: true, aliases: [] },
  })
  const rows = useFieldArray({ control: form.control, name: 'aliases' })
  useEffect(() => {
    if (query.data) form.reset(query.data.config)
  }, [query.data, form])
  const save = useMutation({
    mutationFn: (config: MetadataSourcesConfig) => {
      if (!query.data) throw new Error(t('Failed to load metadata rules'))
      return saveMetadataSources(config, query.data.version)
    },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: metadataSourcesKey })
      props.onSaved()
      props.onClose()
    },
    onError: (error) => handleServerError(error),
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !save.isPending) props.onClose()
      }}
      title={t('Metadata sources and aliases')}
      description={t(
        'Read metadata from each model’s bound channel source, then normalize names. These rules never change price matching or channel routing.'
      )}
      contentClassName='sm:max-w-4xl'
      contentHeight='min(70vh, 760px)'
      footer={
        <>
          <Button
            variant='outline'
            disabled={save.isPending}
            onClick={props.onClose}
          >
            {t('Cancel')}
          </Button>
          <Button
            disabled={!query.data || save.isPending}
            onClick={form.handleSubmit((value) => save.mutate(value))}
          >
            {t('Save metadata rules')}
          </Button>
        </>
      }
    >
      {query.isPending && <LoadingState />}
      {query.isError && (
        <ErrorState
          description={t('Failed to load metadata rules')}
          onRetry={() => void query.refetch()}
        />
      )}
      {query.data && (
        <Form {...form}>
          <div className='space-y-5'>
            <FormField
              control={form.control}
              name='channel_fallback'
              render={({ field }) => (
                <FormItem className='flex items-center justify-between gap-3'>
                  <FormLabel>
                    {t('Supplement from bound channel sources')}
                  </FormLabel>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                </FormItem>
              )}
            />
            <p className='text-muted-foreground text-sm'>
              {t(
                'Channel source URLs and metadata field mappings are maintained in Billing → Model pricing → Price sources. Missing descriptions are left empty; existing metadata is preserved.'
              )}
            </p>
            <p className='text-muted-foreground text-sm'>
              {t(
                'Use one prefix rule to classify a model family, for example grok- → xAI. A target model supplies missing descriptive fields only.'
              )}
            </p>
            {rows.fields.map((row, index) => (
              <div
                key={row.id}
                className='grid gap-3 rounded-md border p-3 sm:grid-cols-2'
              >
                <FormField
                  control={form.control}
                  name={`aliases.${index}.match`}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Match type')}</FormLabel>
                      <Select
                        value={field.value}
                        onValueChange={field.onChange}
                        items={[
                          { value: 'exact', label: t('Exact model name') },
                          { value: 'prefix', label: t('Model name prefix') },
                        ]}
                      >
                        <FormControl>
                          <SelectTrigger
                            aria-label={t('Alias match {{index}}', {
                              index: index + 1,
                            })}
                          >
                            <SelectValue />
                          </SelectTrigger>
                        </FormControl>
                        <SelectContent>
                          <SelectItem value='exact'>
                            {t('Exact model name')}
                          </SelectItem>
                          <SelectItem value='prefix'>
                            {t('Model name prefix')}
                          </SelectItem>
                        </SelectContent>
                      </Select>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name={`aliases.${index}.name`}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Model name or prefix')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          aria-label={t('Alias name {{index}}', {
                            index: index + 1,
                          })}
                          placeholder='grok-'
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name={`aliases.${index}.metadata_model`}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>
                        {t('Metadata target model (optional)')}
                      </FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          aria-label={t('Alias target {{index}}', {
                            index: index + 1,
                          })}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name={`aliases.${index}.vendor`}
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Normalized vendor (optional)')}</FormLabel>
                      <FormControl>
                        <Input
                          {...field}
                          aria-label={t('Alias vendor {{index}}', {
                            index: index + 1,
                          })}
                          placeholder='xAI'
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <Button
                  variant='outline'
                  type='button'
                  className='sm:col-span-2 sm:justify-self-end'
                  disabled={save.isPending}
                  onClick={() => rows.remove(index)}
                  aria-label={t('Remove alias {{index}}', { index: index + 1 })}
                >
                  {t('Remove')}
                </Button>
              </div>
            ))}
            <Button
              variant='outline'
              disabled={save.isPending || rows.fields.length >= 200}
              onClick={() =>
                rows.append({
                  match: 'exact',
                  name: '',
                  metadata_model: '',
                  vendor: '',
                })
              }
            >
              {t('Add metadata alias')}
            </Button>
          </div>
        </Form>
      )}
    </Dialog>
  )
}
