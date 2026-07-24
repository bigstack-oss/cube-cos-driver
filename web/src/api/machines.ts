import { Machine, MachineInput } from '../model/machine'

const jsonOrThrow = async (resp: Response): Promise<unknown> => {
  if (resp.ok) return resp.status === 204 ? undefined : resp.json()
  let message = `${resp.status} ${resp.statusText}`
  try {
    const body = (await resp.json()) as { message?: string }
    if (body.message) message = body.message
  } catch {
    // keep status text
  }
  throw new Error(message)
}

export const listMachines = async (): Promise<Machine[]> =>
  (await jsonOrThrow(await fetch('/api/v1/machines'))) as Machine[]

export const createMachine = async (input: MachineInput): Promise<Machine> =>
  (await jsonOrThrow(
    await fetch('/api/v1/machines', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    }),
  )) as Machine

export const updateMachine = async (
  id: string,
  input: MachineInput,
): Promise<Machine> =>
  (await jsonOrThrow(
    await fetch(`/api/v1/machines/${encodeURIComponent(id)}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(input),
    }),
  )) as Machine

export const deleteMachine = async (id: string): Promise<void> => {
  await jsonOrThrow(
    await fetch(`/api/v1/machines/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  )
}

export const fetchMachineHardware = async (id: string): Promise<void> => {
  await jsonOrThrow(
    await fetch(`/api/v1/machines/${encodeURIComponent(id)}/fetch`, {
      method: 'POST',
    }),
  )
}

export type ImportRowError = { row: number; message: string }
export type ImportResult = { created: number; errors: ImportRowError[] }

export const importMachines = async (file: File): Promise<ImportResult> => {
  const form = new FormData()
  form.append('file', file)
  return (await jsonOrThrow(
    await fetch('/api/v1/machines/import', { method: 'POST', body: form }),
  )) as ImportResult
}

export const importTemplateUrl = '/api/v1/machines/import/template'

export const assignMachine = async (
  id: string,
  clusterId: string,
  hostname: string,
): Promise<void> => {
  await jsonOrThrow(
    await fetch(`/api/v1/machines/${encodeURIComponent(id)}/assignment`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ clusterId, hostname }),
    }),
  )
}

export const unassignMachine = async (id: string): Promise<void> => {
  await jsonOrThrow(
    await fetch(`/api/v1/machines/${encodeURIComponent(id)}/assignment`, {
      method: 'DELETE',
    }),
  )
}
