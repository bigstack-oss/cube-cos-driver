import { CosButton, CosTag, GetCosBasicTable } from '@cube-frontend/ui-library'
import { Machine } from '../../model/machine'
import { ifName, NodeConfig } from '../../model/types'

type NodeRow = {
  id: string
  hostname: string
  role: string
  mgmtIP: string
  interfaces: number
  server: string
  osDisk: string
  node: NodeConfig
}

const Table = GetCosBasicTable<NodeRow>()

export type NodeTableProps = {
  nodes: NodeConfig[]
  // Machine assigned to each hostname (from the hardware inventory).
  serverByHostname: Record<string, Machine>
  // OS install disk chosen per hostname (empty when not yet selected).
  osDiskByHostname: Record<string, string>
  // Absent when the cluster has never been saved to the server.
  snapshotUrlFor?: (hostname: string) => string
  onEdit: (node: NodeConfig) => void
  onDuplicate: (node: NodeConfig) => void
  onDelete: (node: NodeConfig) => void
  onAssignServer: (node: NodeConfig) => void
}

export const NodeTable = (props: NodeTableProps) => {
  const {
    nodes,
    serverByHostname,
    osDiskByHostname,
    snapshotUrlFor,
    onEdit,
    onDuplicate,
    onDelete,
    onAssignServer,
  } = props

  const rows: NodeRow[] = nodes.map((node) => {
    const mgmtId = node.roleSettings.mgmtIF?.id
    const mgmtIF = [...node.initIFs, ...node.bondIFs, ...node.vlanIFs].find(
      (f) => f.id === mgmtId,
    )
    const server = serverByHostname[node.hostname]
    return {
      id: node.id,
      hostname: node.hostname,
      role: node.role,
      mgmtIP: mgmtIF?.IPAddr ?? '—',
      interfaces:
        node.initIFs.length + node.bondIFs.length + node.vlanIFs.length,
      server: server ? server.label : '',
      osDisk: osDiskByHostname[node.hostname] ?? '',
      node,
    }
  })

  return (
    <Table rows={rows}>
      <Table.Column label="Hostname" property="hostname" emphasize />
      <Table.Column label="Role" property="role">
        {(role: string) => (
          <CosTag variant="stroke" color="primary-blue">
            {role}
          </CosTag>
        )}
      </Table.Column>
      <Table.Column label="Management IP" property="mgmtIP" />
      <Table.Column label="Server (BMC)" property="server">
        {(server: string, row: NodeRow) => (
          <CosButton
            type={server ? 'ghost' : 'secondary'}
            size="sm"
            onClick={() => onAssignServer(row.node)}
          >
            {server || 'Assign server'}
          </CosButton>
        )}
      </Table.Column>
      <Table.Column label="OS disk" property="osDisk">
        {(osDisk: string, row: NodeRow) =>
          osDisk ? (
            <span className="primary-body4">{osDisk}</span>
          ) : row.server ? (
            <CosTag variant="stroke" color="dark">
              Not selected
            </CosTag>
          ) : (
            <span className="secondary-body5 text-functional-text-light">—</span>
          )
        }
      </Table.Column>
      <Table.Column label="Interfaces" property="interfaces">
        {(count: number, row: NodeRow) =>
          `${count} (${[...row.node.initIFs, ...row.node.bondIFs, ...row.node.vlanIFs]
            .filter((f) => f.enabled)
            .map((f) => ifName(row.node, f.id))
            .join(', ')})`
        }
      </Table.Column>
      <Table.Column label="Actions" property="id" fitContent>
        {(_: string, row: NodeRow) => (
          <div className="flex gap-x-1">
            <CosButton type="ghost" size="sm" onClick={() => onEdit(row.node)}>
              Edit
            </CosButton>
            <CosButton
              type="ghost"
              size="sm"
              onClick={() => onDuplicate(row.node)}
            >
              Duplicate
            </CosButton>
            {snapshotUrlFor && (
              <a href={snapshotUrlFor(row.hostname)} download>
                <CosButton type="ghost" size="sm">
                  Download
                </CosButton>
              </a>
            )}
            <CosButton
              type="warning"
              size="sm"
              onClick={() => onDelete(row.node)}
            >
              Delete
            </CosButton>
          </div>
        )}
      </Table.Column>
    </Table>
  )
}
