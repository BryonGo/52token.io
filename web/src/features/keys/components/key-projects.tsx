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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getKeyProjects, saveKeyProject, type KeyProject } from '../api'
import { useApiKeys } from './api-keys-provider'

export function KeyProjects() {
  const { t } = useTranslation()
  const client = useQueryClient()
  const { projectId, setProjectId } = useApiKeys()
  const [search, setSearch] = useState('')
  const [edit, setEdit] = useState<KeyProject | null>(null)
  const [saving, setSaving] = useState(false)
  const { data, error, isPending } = useQuery({
    queryKey: ['key-projects'],
    queryFn: async () => requireServerSuccess(await getKeyProjects()),
  })
  async function save() {
    if (!edit) return
    setSaving(true)
    try {
      const result = requireServerSuccess(await saveKeyProject(edit))
      await client.invalidateQueries({ queryKey: ['key-projects'] })
      if (result.data) setProjectId(result.data.id)
      setEdit(null)
    } catch (e) {
      handleServerError(e)
    } finally {
      setSaving(false)
    }
  }
  return (
    <aside
      className='space-y-3 border-b pb-4 lg:w-56 lg:shrink-0 lg:border-r lg:border-b-0 lg:pr-4'
      aria-label={t('Projects')}
    >
      <div className='flex items-center justify-between'>
        <h2 className='font-medium'>{t('Projects')}</h2>
        <Button
          size='sm'
          variant='outline'
          onClick={() => setEdit({ id: 0, name: '' })}
        >
          {t('Add project')}
        </Button>
      </div>
      <Input
        aria-label={t('Search projects')}
        placeholder={t('Search projects')}
        value={search}
        onChange={(e) => setSearch(e.target.value)}
      />
      <nav
        className='max-h-96 space-y-1 overflow-y-auto'
        aria-label={t('Project filter')}
      >
        <Button
          className='w-full justify-start'
          variant={projectId === undefined ? 'secondary' : 'ghost'}
          aria-pressed={projectId === undefined}
          onClick={() => setProjectId(undefined)}
        >
          {t('All projects')}
        </Button>
        <Button
          className='w-full justify-start'
          variant={projectId === 0 ? 'secondary' : 'ghost'}
          aria-pressed={projectId === 0}
          onClick={() => setProjectId(0)}
        >
          {t('Unassigned')}
        </Button>
        {(data?.data || [])
          .filter((p) => p.name.toLowerCase().includes(search.toLowerCase()))
          .map((p) => (
            <div key={p.id} className='flex min-w-0'>
              <Button
                className='min-w-0 flex-1 justify-start truncate'
                variant={projectId === p.id ? 'secondary' : 'ghost'}
                aria-pressed={projectId === p.id}
                onClick={() => setProjectId(p.id)}
              >
                {p.name}
              </Button>
              <Button
                variant='ghost'
                size='sm'
                aria-label={t('Rename project {{name}}', { name: p.name })}
                onClick={() => setEdit({ ...p })}
              >
                {t('Edit')}
              </Button>
            </div>
          ))}
      </nav>
      {isPending && <p>{t('Loading...')}</p>}
      {error && <p role='alert'>{t('Failed to load projects')}</p>}
      <Dialog
        open={Boolean(edit)}
        onOpenChange={(open) => {
          if (!open && !saving) setEdit(null)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {t(edit?.id ? 'Rename project' : 'Add project')}
            </DialogTitle>
            <DialogDescription>
              {t(
                'Projects organize keys and usage reports. They do not change groups, multipliers or routing.'
              )}
            </DialogDescription>
          </DialogHeader>
          {edit && (
            <form
              onSubmit={(e) => {
                e.preventDefault()
                void save()
              }}
              className='space-y-4'
            >
              <Label htmlFor='project-name'>{t('Project name')}</Label>
              <Input
                id='project-name'
                required
                maxLength={80}
                value={edit.name}
                onChange={(e) => setEdit({ ...edit, name: e.target.value })}
              />
              <Button type='submit' disabled={saving || !edit.name.trim()}>
                {saving ? t('Saving...') : t('Save')}
              </Button>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </aside>
  )
}
