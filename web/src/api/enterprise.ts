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
  AdvisorFile: string
  AdvisorLBIP: string
  StorageBackend: string
}

export type Step = {
  Name: string
  Title: string
  State: StepState
  Output: string
  Err: string
  StartedAt?: string
  FinishedAt?: string
}

export type Install = {
  ClusterID: string
  Module: string
  Op?: 'install' | 'uninstall'
  Host?: string
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

export type Artifacts = { AppFW: string[]; CMP: string[]; Advisor: string[] }

export type Module = 'appfw' | 'cmp' | 'advisor'

export type StartInstallBody = {
  module: Module
  params: InstallParams
  manual: boolean
  simulateAirgap: boolean
  password: string
  manifest: string
  // ad-hoc target by VIP instead of a configured cluster (optional)
  vip?: string
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

// The enterprise images folder (App-Framework + CubeCMP + Advisor install
// artifacts). Large; can live on separately-mounted media (USB / virtual
// media), pointed at from the UI — kept apart from the cluster snapshot store.
export type EnterpriseDir = {
  imageDir: string
  mounted: boolean
  appfwCount: number
  cmpCount: number
  advisorCount: number
}

export const getEnterpriseDir = async (): Promise<EnterpriseDir> =>
  (await jsonOrThrow(await fetch('/api/v1/enterprise/dir'))) as EnterpriseDir

// Server-side directory listing so the UI can browse the driver host's
// filesystem to the enterprise images folder (e.g. a mounted USB).
export type DirListing = { path: string; parent: string; dirs: string[] }

export const listDirs = async (path: string): Promise<DirListing> =>
  (await jsonOrThrow(
    await fetch(`/api/v1/fs/dirs?path=${encodeURIComponent(path)}`),
  )) as DirListing

// Block devices (removable media etc.) the operator can mount to reach images.
export type BlockDevice = {
  name: string
  size: string
  fstype: string
  label: string
  mountpoint: string
  removable: boolean
}

export const getDevices = async (): Promise<BlockDevice[]> => {
  const r = (await jsonOrThrow(await fetch('/api/v1/fs/devices'))) as {
    devices?: BlockDevice[]
  }
  return r.devices ?? []
}

// mountDevice mounts a /dev device and returns its mountpoint; throws the
// server's message on failure.
export const mountDevice = async (device: string): Promise<string> => {
  const r = (await jsonOrThrow(
    await fetch('/api/v1/fs/mount', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ device }),
    }),
  )) as { mountpoint: string }
  return r.mountpoint
}

// setEnterpriseDir points the driver at a new enterprise images folder; throws
// the server's message on a bad path (not a directory / not mounted).
export const setEnterpriseDir = async (
  imageDir: string,
): Promise<EnterpriseDir> =>
  (await jsonOrThrow(
    await fetch('/api/v1/enterprise/dir', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ imageDir }),
    }),
  )) as EnterpriseDir

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

export type StartUninstallBody = {
  module: Module
  // framework name to tear down (defaults to "appfw" server-side); the backend
  // needs a name, not the install image params.
  params: { Project?: string; Framework?: string }
  manual: boolean
  // ad-hoc target by VIP instead of a configured cluster (optional)
  vip?: string
}

export const startUninstall = async (
  id: string,
  body: StartUninstallBody,
): Promise<Install> =>
  (await jsonOrThrow(
    await fetch(`/api/v1/clusters/${encodeURIComponent(id)}/enterprise/uninstall`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  )) as Install

// listInstalls returns every known install run across all clusters (dashboard).
export const listInstalls = async (): Promise<Install[]> =>
  (await jsonOrThrow(await fetch('/api/v1/enterprise/installs'))) as Install[]

// StepStats maps a step Name to its median observed duration (seconds) across
// past runs — the data-driven "typical" baseline for the progress view.
export type StepStats = Record<string, number>

export const getStepStats = async (): Promise<StepStats> => {
  const resp = await fetch('/api/v1/enterprise/step-stats')
  if (!resp.ok) return {}
  const body = (await resp.json()) as { stepDurations?: StepStats }
  return body.stepDurations ?? {}
}

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
  vip?: string,
  framework?: string,
): Promise<ClusterInfo> =>
  (await jsonOrThrow(
    await fetch(
      `/api/v1/clusters/${encodeURIComponent(id)}/enterprise/cluster-info`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password, vip, framework }),
      },
    ),
  )) as ClusterInfo
