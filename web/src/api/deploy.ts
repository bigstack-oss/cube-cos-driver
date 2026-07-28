export type DeployState =
  | 'pending'
  | 'bmc-preflight'
  | 'set-boot-pxe'
  | 'power-cycle'
  | 'netbooting'
  | 'preflighting'
  | 'preflight-ok'
  | 'restoring'
  | 'rebooting'
  | 'imaging'
  | 'imaged'
  | 'checked-in'
  | 'waiting-controller'
  | 'net-preflight'
  | 'applying'
  | 'applied'
  | 'done'
  | 'error'

// Traffic-light status for the two deploy gates (green light 1 = network
// preflight, green light 2 = apply).
export type Light = 'off' | 'yellow' | 'green' | 'red'

// Coarse per-node phase shown in the deploy UI.
export type Phase =
  | 'boot'
  | 'preflight-net'
  | 'time'
  | 'wait-for-master'
  | 'applying'
  | 'done'
  | 'error'

export type PreflightResult = { target: string; ok: boolean; detail?: string }

export type NodePreflight = {
  carrierOk: boolean
  clockSkewSec: number
  matrix?: PreflightResult[]
  passed: boolean
  reportedAt: string
}

export type NodeDeploy = {
  hostname: string
  machineId: string
  state: DeployState
  message?: string
  errCode?: string
  preflight?: PreflightResult[]
  installerPreflight?: NodePreflight
  updatedAt: string
  light1: Light
  light2: Light
  phase: Phase
  progress?: PhaseCell[]
}

// One cell of the per-node progress strip: preflight → restore → reboot → apply.
export type CellStatus = 'pending' | 'active' | 'done' | 'error'
export type PhaseCell = { name: string; status: CellStatus }

export type Deploy = {
  clusterId: string
  startedAt: string
  nodes: Record<string, NodeDeploy>
}

export type PlanRow = {
  hostname: string
  assigned: boolean
  machineLabel?: string
  bmcAddress?: string
  osDisk?: string
  macs?: string[]
  /** Another cluster whose active deploy already claims this machine. */
  conflict?: string
}

export type Plan = { allAssigned: boolean; nodes: PlanRow[] }

// getDeployPlan returns the plan body whether or not all nodes are assigned
// (the endpoint uses 409 to signal "not all assigned" but still returns rows).
export const getDeployPlan = async (id: string): Promise<Plan> => {
  const resp = await fetch(`/api/v1/clusters/${encodeURIComponent(id)}/deploy/plan`)
  return (await resp.json()) as Plan
}

const throwOnError = async (resp: Response): Promise<unknown> => {
  if (resp.ok) {
    if (resp.status === 204) return undefined
    try {
      return await resp.json()
    } catch {
      return undefined // empty/non-JSON success body is fine
    }
  }
  let message = `${resp.status} ${resp.statusText}`
  try {
    const b = (await resp.json()) as { message?: string }
    if (b.message) message = b.message
  } catch {
    // keep status text
  }
  throw new Error(message)
}

export const startDeploy = async (
  id: string,
  hostnames?: string[],
): Promise<void> => {
  await throwOnError(
    await fetch(`/api/v1/clusters/${encodeURIComponent(id)}/deploy`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ confirm: true, hostnames }),
    }),
  )
}

export const getDeployStatus = async (id: string): Promise<Deploy | null> => {
  const resp = await fetch(`/api/v1/clusters/${encodeURIComponent(id)}/deploy`)
  if (resp.status === 404) return null
  return (await throwOnError(resp)) as Deploy
}

export const cancelDeploy = async (id: string): Promise<void> => {
  await throwOnError(
    await fetch(`/api/v1/clusters/${encodeURIComponent(id)}/deploy/cancel`, {
      method: 'POST',
    }),
  )
}

// A deploy node is done stepping when it reaches a terminal state.
export const isTerminal = (s: DeployState): boolean =>
  s === 'done' || s === 'error'

// set_ready (FTS finalize) — the operator supplies the external network; the
// master agent polls this and runs `cluster set_ready`.
export type SetReady = {
  trigger: boolean
  createExternal: boolean
  cidr: string
  gateway: string
  ipRange: string
  ready: boolean
  message?: string
}

export const getSetReady = async (id: string): Promise<SetReady | null> => {
  const resp = await fetch(`/api/v1/clusters/${encodeURIComponent(id)}/set-ready`)
  if (!resp.ok) return null
  return (await resp.json()) as SetReady
}

export const submitSetReady = async (
  id: string,
  input: { createExternal: boolean; cidr: string; gateway: string; ipRange: string },
): Promise<void> => {
  await throwOnError(
    await fetch(`/api/v1/clusters/${encodeURIComponent(id)}/set-ready`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    }),
  )
}
