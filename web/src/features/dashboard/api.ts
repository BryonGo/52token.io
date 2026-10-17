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
import { api } from '@/lib/api'

import type {
  FlowQuotaDataItem,
  QuotaDataItem,
  UptimeGroupResult,
} from './types'

export type ModelHealthRow = {
  channel_id: number
  channel_name: string
  model: string
  groups: Array<{ id: string; name: string }>
  state:
    | 'unknown'
    | 'unverified'
    | 'healthy'
    | 'unhealthy'
    | 'interface_only'
    | 'stale'
    | 'disabled'
  routed: boolean
  checked_at: number
  latency_ms: number
  success_rate: number | null
  checks: number
  method: string
}

export async function getModelHealth() {
  const response = await api.get<{ success: boolean; data: ModelHealthRow[] }>(
    '/api/channel/model-health'
  )
  return response.data
}

// ============================================================================
// Dashboard APIs
// ============================================================================

// ----------------------------------------------------------------------------
// Quota & Usage Data
// ----------------------------------------------------------------------------

// Get user quota data within a time range
// Admin users get all users' data by default.
export async function getUserQuotaDates(
  params: {
    start_timestamp: number
    end_timestamp: number
    default_time?: string
    username?: string
  },
  isAdmin = false
) {
  const endpoint = isAdmin ? '/api/data' : '/api/data/self'
  const res = await api.get<{ success: boolean; data: QuotaDataItem[] }>(
    endpoint,
    { params }
  )
  return res.data
}

// ----------------------------------------------------------------------------
// System Monitoring
// ----------------------------------------------------------------------------

export async function getUserQuotaDataByUsers(params: {
  start_timestamp: number
  end_timestamp: number
}) {
  const res = await api.get<{ success: boolean; data: QuotaDataItem[] }>(
    '/api/data/users',
    { params }
  )
  return res.data
}

export async function getFlowQuotaDates(
  params: {
    start_timestamp: number
    end_timestamp: number
    default_time?: string
    username?: string
  },
  isAdmin = false
) {
  const endpoint = isAdmin ? '/api/data/flow' : '/api/data/flow/self'
  const res = await api.get<{
    success: boolean
    data?: FlowQuotaDataItem[]
    message?: string
  }>(endpoint, { params })
  return res.data
}

// Get uptime monitoring status for all services
export async function getUptimeStatus() {
  const res = await api.get<{ success: boolean; data: UptimeGroupResult[] }>(
    '/api/uptime/status'
  )
  return res.data
}

export type ProjectUsageAmount = {
  name: string
  quota: number
  requests: number
}
export type ProjectUsageReport = {
  total_quota: number
  period_quota: number
  requests: number
  timezone: string
  daily: ProjectUsageAmount[]
  families: ProjectUsageAmount[]
  models: ProjectUsageAmount[]
}
export async function getProjectUsage(
  projectId: number | undefined,
  days: number
): Promise<{ success: boolean; message?: string; data?: ProjectUsageReport }> {
  const params = new URLSearchParams({ days: String(days) })
  if (projectId !== undefined) params.set('project_id', String(projectId))
  return (await api.get(`/api/token/project-usage?${params}`)).data
}
