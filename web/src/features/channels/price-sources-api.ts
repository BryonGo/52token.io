import { useQuery } from '@tanstack/react-query'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export const SOURCE_FIELD_LABELS = {
  description: 'Metadata description field',
  vendor: 'Metadata vendor name field',
  vendor_id: 'Metadata vendor ID field',
  icon: 'Metadata icon field',
  tags: 'Metadata tags field',
  endpoints: 'Metadata endpoint types field',
  vendors: 'Metadata vendor list path',
  vendor_key: 'Vendor list ID field',
  vendor_name: 'Vendor list name field',
  vendor_icon: 'Vendor list icon field',
  request_price: 'Price per request field',
  request_unit: 'Request price unit field',
  request_units: 'Request price unit count field',
  usage_schema: 'Upstream usage schema field',
  quota_type: 'Billing unit field',
  items: 'Model list path',
  model: 'Model name field',
  input: 'Input price field',
  output: 'Output price field',
  cache_read: 'Cache read price field',
  cache_write: 'Cache write price field',
  cache_write_1h: 'Cache write 1h field',
  billing_mode: 'Billing mode field',
  expression: 'Billing expression field',
  groups: 'Available groups field',
  group_ratios: 'Group multipliers path',
  quota_unit: 'Quota unit path in status response',
  success: 'Success flag path',
} as const
export type SourceField = keyof typeof SOURCE_FIELD_LABELS
export type PriceSource = {
  local_group_billing?: boolean
  model_rules?: Record<
    string,
    {
      expression: string
      usage_schema: Record<string, Record<string, unknown>>
      document_url: string
    }
  >

  advanced_pricing?: boolean
  id: string
  name: string
  enabled: boolean
  format: 'json' | 'newapi' | 'sub2api' | 'deepseek_html'
  url: string
  channel_origin: string
  filter_field?: string
  filter_value?: string
  filter_exclude?: boolean
  auth_configured?: boolean
  auth_user_id?: number
  price_currency?: 'CNY' | 'USD' | 'CREDIT'
  default_credits_per_cny?: number
  status_url: string
  unit_tokens: number
  input_scale: number
  output_ratio: boolean
  cache_ratio: boolean
  fields: Record<SourceField, string>
  reference?: {
    group_ratio: number
    models: Record<string, string>
    schedule: 'deepseek' | 'off_peak'
    local_group_billing: boolean
  }
}
export const EMPTY_SOURCE_FIELDS = Object.fromEntries(
  Object.keys(SOURCE_FIELD_LABELS).map((key) => [key, ''])
) as Record<SourceField, string>
export const priceSourcesKey = ['channel-price-sources']
export function usePriceSources(enabled = true) {
  return useQuery({
    queryKey: priceSourcesKey,
    queryFn: async () =>
      requireServerSuccess((await api.get('/api/channel/price-sources')).data)
        .data as { sources: PriceSource[]; version: string },
    enabled,
  })
}
