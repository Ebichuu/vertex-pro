import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { InstanceGroups } from './InstanceGroups'
import { rulesClient } from '../../features/rules/api'

afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe('instance groups', () => {
  it('saves selected downloader members with the current group revision', async () => {
    const save = vi.spyOn(rulesClient, 'saveInstanceGroup').mockResolvedValue({})
    const onSaved = vi.fn().mockResolvedValue(undefined)
    render(<InstanceGroups groups={[{ id: 'both', name: '双机', members: ['qb-a'], revision: 3 }]} instances={[{ id: 'qb-a', label: 'qB A' }, { id: 'qb-b', label: 'qB B' }]} onSaved={onSaved} />)
    fireEvent.click(screen.getByText('管理 qB 分流组'))
    fireEvent.change(screen.getByRole('combobox', { name: '编辑分流组' }), { target: { value: 'both' } })
    fireEvent.click(screen.getByRole('checkbox', { name: 'qB B' }))
    fireEvent.click(screen.getByRole('button', { name: '保存分流组' }))
    await waitFor(() => expect(save).toHaveBeenCalled())
    expect(save.mock.calls[0][0]).toMatchObject({ body: { id: 'both', name: '双机', revision: 3, members: ['qb-a', 'qb-b'] } })
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1))
  })
})
