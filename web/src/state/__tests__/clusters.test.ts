// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from 'vitest'
import { ha3 } from '../../testing/fixtures'
import {
  loadClusterConfig,
  loadClustersInfo,
  loadNodes,
  removeClusterDraft,
  saveClustersInfo,
  writeClusterDraft,
} from '../clusters'

describe('cluster draft storage', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('round-trips clustersInfo', () => {
    expect(loadClustersInfo()).toEqual([])
    saveClustersInfo([{ id: 'abc', name: 'one' }])
    expect(loadClustersInfo()).toEqual([{ id: 'abc', name: 'one' }])
  })

  it('writes and removes full drafts with legacy keys', () => {
    writeClusterDraft(ha3)
    const id = ha3.clusterInfo.id
    expect(loadClustersInfo()).toHaveLength(1)
    expect(localStorage.getItem(`${id}-cluster`)).toBeTruthy()
    expect(localStorage.getItem(`${id}-nodes`)).toBeTruthy()
    expect(loadClusterConfig(id)?.HA).toBe(true)
    expect(loadNodes(id)).toHaveLength(3)

    removeClusterDraft(id)
    expect(loadClusterConfig(id)).toBeNull()
    expect(loadNodes(id)).toEqual([])
  })

  it('writeClusterDraft replaces an existing entry instead of duplicating', () => {
    writeClusterDraft(ha3)
    writeClusterDraft({
      ...ha3,
      clusterInfo: { ...ha3.clusterInfo, name: 'renamed' },
    })
    const infos = loadClustersInfo()
    expect(infos).toHaveLength(1)
    expect(infos[0].name).toBe('renamed')
  })

  it('tolerates corrupted JSON', () => {
    localStorage.setItem('clustersInfo', '{oops')
    expect(loadClustersInfo()).toEqual([])
  })
})
