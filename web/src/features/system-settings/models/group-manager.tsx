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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { GroupBadge } from '@/components/group-badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/api'
import { invalidateGroupQueries } from '@/lib/group-queries'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useGroupLabelStore } from '@/stores/group-label-store'

type ManagedGroup = {
  id: string
  name: string
  ratio: number
  selectable: boolean
  description: string
  defined: boolean
  channels: { id: number; name: string }[]
  token_count: number
  user_count: number
}
type Catalog = { version: string; groups: ManagedGroup[] }
const blank: ManagedGroup = {
  id: '',
  name: '',
  ratio: 1,
  selectable: true,
  description: '',
  defined: true,
  channels: [],
  token_count: 0,
  user_count: 0,
}

export function GroupManager() {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [search, setSearch] = useState('')
  const [edit, setEdit] = useState<ManagedGroup | null>(null)
  const [version, setVersion] = useState('')
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState<ManagedGroup | null>(null)
  const { data, isPending, error, refetch } = useQuery({
    queryKey: ['managed-groups'],
    queryFn: async () =>
      requireServerSuccess((await api.get('/api/group/managed')).data)
        .data as Catalog,
  })
  useEffect(() => {
    if (!data) return
    useGroupLabelStore.setState((s) => ({
      labels: {
        ...s.labels,
        ...Object.fromEntries(data.groups.map((g) => [g.id, { name: g.name }])),
      },
    }))
  }, [data])
  async function save() {
    if (!edit) return
    setSaving(true)
    try {
      const saved = requireServerSuccess(
        (await api.put('/api/group/managed', { ...edit, version })).data
      )
      useGroupLabelStore.setState((s) => ({
        labels: { ...s.labels, [saved.data.id]: { name: edit.name } },
      }))
      setEdit(null)
      await Promise.all([
        refetch(),
        invalidateGroupQueries(client),
        client.invalidateQueries({ queryKey: ['system-options'] }),
      ])
      toast.success(t('Group saved'))
    } catch (e) {
      handleServerError(e)
    } finally {
      setSaving(false)
    }
  }
  async function remove() {
    if (!deleting) return
    setSaving(true)
    try {
      requireServerSuccess(
        (
          await api.delete('/api/group/managed', {
            data: { id: deleting.id, version },
          })
        ).data
      )
      useGroupLabelStore.setState((s) => {
        const labels = { ...s.labels }
        delete labels[deleting.id]
        return { labels }
      })
      setDeleting(null)
      await Promise.all([
        refetch(),
        invalidateGroupQueries(client),
        client.invalidateQueries({ queryKey: ['system-options'] }),
      ])
      toast.success(t('Group deleted'))
    } catch (e) {
      handleServerError(e)
    } finally {
      setSaving(false)
    }
  }
  return (
    <div className='space-y-4'>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Group IDs stay fixed. Renaming a group updates its labels without changing bindings or multipliers. Projects organize keys separately.'
        )}
      </p>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Groups may share a name and multiplier. Add an account or route label to distinguish them. Fixed IDs keep their model prices separate.'
        )}
      </p>
      <div className='flex gap-3'>
        <Input
          aria-label={t('Search groups')}
          placeholder={t('Search groups')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <Button
          disabled={!data}
          onClick={() => {
            setVersion(data?.version || '')
            setEdit({ ...blank })
          }}
        >
          {t('Add group')}
        </Button>
        <Button variant='outline' onClick={() => void refetch()}>
          {t('Refresh')}
        </Button>
      </div>
      {error && <p role='alert'>{t('Failed to load groups')}</p>}
      <StaticDataTable
        data={(data?.groups || []).filter((g) =>
          `${g.id} ${g.name} ${g.channels.map((c) => c.name).join(' ')}`
            .toLowerCase()
            .includes(search.toLowerCase())
        )}
        getRowKey={(g) => g.id}
        emptyContent={isPending ? t('Loading...') : t('No groups found')}
        columns={[
          {
            id: 'id',
            header: t('ID'),
            cell: (g) => (
              <code className='text-muted-foreground text-xs break-all'>
                {g.id}
              </code>
            ),
          },
          {
            id: 'name',
            header: t('Group name'),
            cell: (g) => <GroupBadge group={g.id} label={g.name} />,
          },
          {
            id: 'ratio',
            header: t('Ratio'),
            cell: (g) => (g.defined ? `${g.ratio}×` : t('Unregistered group')),
          },
          {
            id: 'channels',
            header: t('Channels'),
            cell: (g) => g.channels.map((c) => c.name).join(' · ') || '—',
          },
          { id: 'keys', header: t('API Keys'), cell: (g) => g.token_count },
          {
            id: 'selectable',
            header: t('User selectable'),
            cell: (g) => (g.selectable ? t('Yes') : t('No')),
          },
          {
            id: 'edit',
            header: t('Actions'),
            cell: (g) => (
              <div className='flex gap-1'>
                <Button
                  variant='ghost'
                  disabled={!g.defined}
                  onClick={() => {
                    setVersion(data?.version || '')
                    setEdit({ ...g })
                  }}
                >
                  {t('Edit')}
                </Button>
                <Button
                  variant='ghost'
                  disabled={g.id === 'default' || saving}
                  onClick={() => {
                    setVersion(data?.version || '')
                    setDeleting(g)
                  }}
                >
                  {t('Delete')}
                </Button>
              </div>
            ),
          },
        ]}
      />
      <ConfirmDialog
        open={Boolean(deleting)}
        onOpenChange={(open) => {
          if (!open && !saving) setDeleting(null)
        }}
        title={t('Delete group')}
        desc={t(
          'Delete this group? Groups used by channels, users or keys must be unbound first.'
        )}
        confirmText={t('Confirm deletion')}
        destructive
        isLoading={saving}
        handleConfirm={() => void remove()}
      />
      <Sheet
        open={Boolean(edit)}
        onOpenChange={(open) => {
          if (!open && !saving) setEdit(null)
        }}
      >
        <SheetContent className='overflow-y-auto p-6'>
          <SheetHeader>
            <SheetTitle>{t('Group management')}</SheetTitle>
            <SheetDescription>
              {t(
                'The fixed ID is used by channels and keys. The name is only for display.'
              )}
            </SheetDescription>
          </SheetHeader>
          {edit && (
            <form
              className='space-y-5'
              onSubmit={(e) => {
                e.preventDefault()
                void save()
              }}
            >
              <div className='space-y-2'>
                <Label htmlFor='group-id'>{t('Fixed group ID')}</Label>
                <Input
                  id='group-id'
                  readOnly
                  value={edit.id || t('Generated automatically')}
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor='group-display-name'>{t('Group name')}</Label>
                <Input
                  id='group-display-name'
                  required
                  maxLength={80}
                  value={edit.name}
                  onChange={(e) => setEdit({ ...edit, name: e.target.value })}
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor='group-ratio'>{t('Ratio')}</Label>
                <Input
                  id='group-ratio'
                  type='number'
                  required
                  min={0}
                  step='any'
                  value={edit.ratio}
                  onChange={(e) =>
                    setEdit({ ...edit, ratio: e.target.valueAsNumber })
                  }
                />
              </div>
              <div className='flex items-center justify-between'>
                <Label htmlFor='group-selectable'>{t('User selectable')}</Label>
                <Switch
                  id='group-selectable'
                  checked={edit.selectable}
                  onCheckedChange={(selectable) =>
                    setEdit({ ...edit, selectable })
                  }
                />
              </div>
              <div className='space-y-2'>
                <Label htmlFor='group-description'>{t('Description')}</Label>
                <Input
                  id='group-description'
                  maxLength={1000}
                  value={edit.description}
                  onChange={(e) =>
                    setEdit({ ...edit, description: e.target.value })
                  }
                />
              </div>
              <Button
                type='submit'
                disabled={
                  saving || !edit.name.trim() || !Number.isFinite(edit.ratio)
                }
              >
                {saving ? t('Saving...') : t('Save')}
              </Button>
            </form>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
}
