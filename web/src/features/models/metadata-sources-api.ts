import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export type MetadataAlias = {
  match: 'exact' | 'prefix'
  name: string
  metadata_model: string
  vendor: string
}
export type MetadataSourcesConfig = {
  channel_fallback: boolean
  aliases: MetadataAlias[]
}
export type MetadataSourcesResponse = {
  config: MetadataSourcesConfig
  version: string
}
export const metadataSourcesKey = ['model-metadata-sources']
export async function getMetadataSources(): Promise<MetadataSourcesResponse> {
  return requireServerSuccess(
    (await api.get('/api/models/metadata-sources')).data
  ).data
}
export async function saveMetadataSources(
  config: MetadataSourcesConfig,
  version: string
) {
  return requireServerSuccess(
    (
      await api.put('/api/models/metadata-sources', {
        config,
        expected_version: version,
      })
    ).data
  )
}
