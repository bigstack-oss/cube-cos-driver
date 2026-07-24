export type DeployState =
  | 'pending'
  | 'bmc-preflight'
  | 'set-boot-pxe'
  | 'power-cycle'
  | 'netbooting'
  | 'imaging'
  | 'imaged'
  | 'checked-in'
  | 'net-preflight'
  | 'applying'
  | 'applied'
  | 'done'
  | 'error'

export type PreflightResult = { target: string; ok: boolean; detail?: string }

export type NodeDeploy = {
  hostname: string
  machineId: string
  state: DeployState
  message?: string
  preflight?: PreflightResult[]
  updatedAt: string
}

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
