// Thin fetch wrappers for the /api/v1 enterprise-module-install endpoints.
// The Go Install/Step/InstallParams/Artifacts structs have no json tags, so
// their wire keys are the Go field names verbatim (PascalCase). Only the POST
// request body's top-level keys are lowercase (the handler's request struct
// IS tagged).

export type StepState = 'pending' | 'active' | 'done' | 'error' | 'skipped'
export type InstallState = 'running' | 'done' | 'error' | 'cancelled'

export type InstallParams = {
  Project: string
  PublicNet: string
  MgmtNet: string
  LBIP: string
  OSImage: string
  Framework: string
  AppFile: string
  FsImage: string
  LBImage: string
  StorageBackend: string
}

export type Step = {
  Name: string
  Title: string
  State: StepState
  Output: string
  Err: string
}

export type Install = {
  ClusterID: string
  Module: string
  StartedAt: string
  Manual: boolean
  ManualStep: number
  SimulateAirgap: boolean
  Params: InstallParams
  Steps: Step[]
  Current: number
  State: InstallState
  Portal: string
}

export type Artifacts = { AppFW: string[]; CMP: string[] }

export type Module = 'appfw' | 'cmp'

export type StartInstallBody = {
  module: Module
  params: InstallParams
  manual: boolean
  simulateAirgap: boolean
  password: string
  manifest: string
}

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

export const getArtifacts = async (): Promise<Artifacts> =>
  (await jsonOrThrow(await fetch('/api/v1/enterprise/artifacts'))) as Artifacts

export const startInstall = async (
  id: string,
  body: StartInstallBody,
): Promise<Install> =>
  (await jsonOrThrow(
    await fetch(`/api/v1/clusters/${encodeURIComponent(id)}/enterprise/install`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  )) as Install

// listInstalls returns every known install run across all clusters (dashboard).
export const listInstalls = async (): Promise<Install[]> =>
  (await jsonOrThrow(await fetch('/api/v1/enterprise/installs'))) as Install[]

export const getInstall = async (
  id: string,
  module: Module,
): Promise<Install> =>
  (await jsonOrThrow(
    await fetch(
      `/api/v1/clusters/${encodeURIComponent(id)}/enterprise/install?module=${encodeURIComponent(module)}`,
    ),
  )) as Install

export const nextStep = async (id: string, module: Module): Promise<Install> =>
  (await jsonOrThrow(
    await fetch(
      `/api/v1/clusters/${encodeURIComponent(id)}/enterprise/install/step/next?module=${encodeURIComponent(module)}`,
      { method: 'POST' },
    ),
  )) as Install

export const cancelInstall = async (id: string, module: Module): Promise<void> => {
  await jsonOrThrow(
    await fetch(
      `/api/v1/clusters/${encodeURIComponent(id)}/enterprise/install/cancel?module=${encodeURIComponent(module)}`,
      { method: 'POST' },
    ),
  )
}

// ClusterInfo is the selected cluster's OpenStack projects + networks (and
// whether it supports air-gap simulation), used to populate the install form.
export type ClusterInfo = {
  projects: string[]
  networks: string[]
  airgapSupported: boolean
  suggestedLBIP: string
  suggestedStorage: string
  version: string
  manifest: string
  manifests: string[]
}

export const clusterInfo = async (
  id: string,
  password: string,
): Promise<ClusterInfo> =>
  (await jsonOrThrow(
    await fetch(
      `/api/v1/clusters/${encodeURIComponent(id)}/enterprise/cluster-info`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password }),
      },
    ),
  )) as ClusterInfo
