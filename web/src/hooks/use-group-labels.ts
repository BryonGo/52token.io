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
import { useEffect } from 'react'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'
import {
  useGroupLabelStore,
  type GroupLabels,
} from '@/stores/group-label-store'

export function useGroupLabels() {
  const session = useAuthStore((s) => s.auth.session?.sid)
  const userId = useAuthStore((s) => s.auth.user?.id)
  const { data } = useQuery({
    queryKey: ['group-labels', userId, session],
    enabled: Boolean(userId),
    queryFn: async () =>
      requireServerSuccess((await api.get('/api/token/group-labels')).data)
        .data as GroupLabels,
    refetchInterval: 60_000,
  })
  useEffect(() => {
    useGroupLabelStore.setState({ labels: data ?? {} })
  }, [data, userId, session])
}
