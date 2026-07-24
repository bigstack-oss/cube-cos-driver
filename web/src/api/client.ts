// Thin fetch wrappers for the /api/v1 endpoints. Errors carry the server's
// JSON {message}.
import { ClusterDetail, ClusterDigest, shortId } from '../model/types'

const jsonOrThrow = async (resp: Response): Promise<unknown> => {
  if (resp.ok) {
    return resp.status === 204 ? undefined : resp.json()
  }
  let message = `${resp.status} ${resp.statusText}`
  try {
    const body = (await resp.json()) as { message?: string }
    if (body.message) message = body.message
  } catch {
    // keep the status text
  }
  throw new Error(message)
}

export const listClusters = async (): Promise<ClusterDigest[]> =>
  (await jsonOrThrow(await fetch('/api/v1/clusters'))) as ClusterDigest[]

// Returns null when the cluster is not stored on the server.
export const getCluster = async (
  id: string,
): Promise<ClusterDetail | null> => {
  const resp = await fetch(`/api/v1/clusters/${encodeURIComponent(id)}`)
  if (resp.status === 404) return null
  return (await jsonOrThrow(resp)) as ClusterDetail
}

export const saveCluster = async (detail: ClusterDetail): Promise<void> => {
  const id = shortId(detail.clusterInfo.id)
  await jsonOrThrow(
    await fetch(`/api/v1/clusters/${encodeURIComponent(id)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(detail),
    }),
  )
}

export const deleteCluster = async (id: string): Promise<void> => {
  const resp = await fetch(`/api/v1/clusters/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
  if (resp.status !== 404) {
    await jsonOrThrow(resp)
  }
}

export const clusterZipUrl = (id: string): string =>
  `/api/v1/clusters/${encodeURIComponent(id)}/download`

export const nodeSnapshotUrl = (id: string, hostname: string): string =>
  `/api/v1/clusters/${encodeURIComponent(id)}/nodes/${encodeURIComponent(hostname)}/download`

// URL a node uses via `snapshot pull url` — served flat by the pxeserver
// web root (lighttpd on :80) when the server runs with --export-dir.
export const pullUrl = (host: string, hostname: string): string =>
  `http://${host}/${hostname}.snapshot`
