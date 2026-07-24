import { IFInfo, NodeRole, NodeRoleSettings } from './types'

export const nodeRoles: NodeRole[] = [
  'control',
  'compute',
  'storage',
  'control-converged',
  'edge-core',
  'moderator',
]

export type RoleIFKey = keyof NodeRoleSettings

// Which role interfaces each role configures (legacy roleOptions).
export const roleOptions: Record<NodeRole, RoleIFKey[]> = {
  control: ['mgmtIF', 'storIF', 'storIFBackend'],
  compute: ['mgmtIF', 'providerIF', 'overlayIF', 'storIF', 'storIFBackend'],
  storage: ['mgmtIF', 'storIF', 'storIFBackend'],
  'control-converged': [
    'mgmtIF',
    'providerIF',
    'overlayIF',
    'storIF',
    'storIFBackend',
  ],
  'edge-core': ['mgmtIF', 'providerIF', 'overlayIF', 'storIF', 'storIFBackend'],
  moderator: ['mgmtIF', 'storIF', 'storIFBackend'],
}

export const hasControlFunc = (role: NodeRole): boolean =>
  ['control', 'control-converged', 'edge-core', 'moderator'].includes(role)

export const roleIFPrompts: Record<
  RoleIFKey,
  { label: string; isRequired: boolean; info: string }
> = {
  mgmtIF: {
    label: 'Management interface',
    isRequired: true,
    info: 'An interface that gives you access to CubeCOS management web interface, CLI & root shell. The IP address of the management interface must be specified.',
  },
  providerIF: {
    label: 'Provider interface',
    isRequired: true,
    info: 'An interface which comes with internet connection.',
  },
  overlayIF: {
    label: 'Overlay interface',
    isRequired: true,
    info: 'An interface for Software-Defined-Network (SDN) and network virtualization.',
  },
  storIF: {
    label: 'Storage frontend interface',
    isRequired: true,
    info: 'An interface for IaaS connection, read/write data over Software-Defined-Storage (SDS).',
  },
  storIFBackend: {
    label: 'Storage backend interface (optional)',
    isRequired: false,
    info: 'A separated interface for data replication of Software-Defined-Storage (SDS).',
  },
}

export const createDefaultRoleSettings = (
  role: NodeRole,
  old?: Partial<NodeRoleSettings>,
): NodeRoleSettings => {
  const settings: Record<string, IFInfo> = {}
  for (const key of roleOptions[role]) {
    settings[key] = old?.[key] ?? {}
  }
  return settings as unknown as NodeRoleSettings
}
