// Draft state in localStorage, using the same keys as the legacy app
// ('clustersInfo', '<id>-cluster', '<id>-nodes') so existing browsers keep
// their drafts. Saving to the server is an explicit user action.
import { useCallback, useEffect, useState } from 'react'
import {
  ClusterConfig,
  ClusterDetail,
  ClusterInfo,
  NodeConfig,
} from '../model/types'

const read = <T>(key: string, fallback: T): T => {
  try {
    const raw = localStorage.getItem(key)
    return raw ? (JSON.parse(raw) as T) : fallback
  } catch {
    return fallback
  }
}

export const loadClustersInfo = (): ClusterInfo[] => read('clustersInfo', [])

export const saveClustersInfo = (infos: ClusterInfo[]): void => {
  localStorage.setItem('clustersInfo', JSON.stringify(infos))
}

export const loadClusterConfig = (id: string): ClusterConfig | null =>
  read<ClusterConfig | null>(`${id}-cluster`, null)

export const saveClusterConfig = (id: string, config: ClusterConfig): void => {
  localStorage.setItem(`${id}-cluster`, JSON.stringify(config))
}

export const loadNodes = (id: string): NodeConfig[] => read(`${id}-nodes`, [])

export const saveNodes = (id: string, nodes: NodeConfig[]): void => {
  localStorage.setItem(`${id}-nodes`, JSON.stringify(nodes))
}

export const removeClusterDraft = (id: string): void => {
  localStorage.removeItem(`${id}-cluster`)
  localStorage.removeItem(`${id}-nodes`)
}

// Import/hydrate a full clusterDetail into the local draft store.
export const writeClusterDraft = (detail: ClusterDetail): void => {
  const infos = loadClustersInfo().filter(
    (i) => i.id !== detail.clusterInfo.id,
  )
  saveClustersInfo([...infos, detail.clusterInfo])
  saveClusterConfig(detail.clusterInfo.id, detail.clusterConfig)
  saveNodes(detail.clusterInfo.id, detail.nodeData)
}

export const useClustersInfo = () => {
  const [clustersInfo, setClustersInfoState] = useState<ClusterInfo[]>(
    loadClustersInfo,
  )
  const setClustersInfo = useCallback((infos: ClusterInfo[]) => {
    saveClustersInfo(infos)
    setClustersInfoState(infos)
  }, [])
  return { clustersInfo, setClustersInfo }
}

export const useClusterDraft = (id: string | undefined) => {
  const [config, setConfigState] = useState<ClusterConfig | null>(() =>
    id ? loadClusterConfig(id) : null,
  )
  const [nodes, setNodesState] = useState<NodeConfig[]>(() =>
    id ? loadNodes(id) : [],
  )

  useEffect(() => {
    setConfigState(id ? loadClusterConfig(id) : null)
    setNodesState(id ? loadNodes(id) : [])
  }, [id])

  const setConfig = useCallback(
    (next: ClusterConfig) => {
      if (!id) return
      saveClusterConfig(id, next)
      setConfigState(next)
    },
    [id],
  )
  const setNodes = useCallback(
    (next: NodeConfig[]) => {
      if (!id) return
      saveNodes(id, next)
      setNodesState(next)
    },
    [id],
  )

  return { config, setConfig, nodes, setNodes }
}
