import { CosButton, CosModal } from '@cube-frontend/ui-library'
import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams } from 'react-router'
import {
  clusterZipUrl,
  deleteCluster,
  getCluster,
  nodeSnapshotUrl,
  saveCluster,
} from '../../api/client'
import { assignMachine, listMachines } from '../../api/machines'
import { Machine } from '../../model/machine'
import { ClusterWizard } from '../../components/wizards/cluster/ClusterWizard'
import { NodeWizard } from '../../components/wizards/node/NodeWizard'
import { AssignServerFlow } from './assign/AssignServerFlow'
import { DeployModal } from './deploy/DeployModal'
import { DeployProgress } from './deploy/DeployProgress'
import { ClusterDetail, NodeConfig, shortId } from '../../model/types'
import { validateCluster } from '../../model/validate'
import {
  loadClustersInfo,
  removeClusterDraft,
  useClusterDraft,
  useClustersInfo,
  writeClusterDraft,
} from '../../state/clusters'
import { newId } from '../../utils/random'
import { ClusterDetailCard } from './ClusterDetailCard'
import { NodeTable } from './NodeTable'
import { ProblemBanner } from './ProblemBanner'
import { SnapshotUrlModal } from './SnapshotUrlModal'

type SaveState = 'unsaved' | 'saving' | 'saved' | 'error'

export const ClusterPage = () => {
  const { id: routeId } = useParams()
  const navigate = useNavigate()
  const { clustersInfo, setClustersInfo } = useClustersInfo()

  // The route uses the short id; local drafts key on the full id.
  const info = clustersInfo.find((i) => shortId(i.id) === routeId)
  const { config, setConfig, nodes, setNodes } = useClusterDraft(info?.id)

  const [clusterWizardOpen, setClusterWizardOpen] = useState(false)
  const [nodeWizardOpen, setNodeWizardOpen] = useState(false)
  const [editingNode, setEditingNode] = useState<NodeConfig | undefined>()
  const [deletingNode, setDeletingNode] = useState<NodeConfig | undefined>()
  const [deleteClusterOpen, setDeleteClusterOpen] = useState(false)
  const [urlModalOpen, setUrlModalOpen] = useState(false)
  const [saveState, setSaveState] = useState<SaveState>('unsaved')
  const [saveError, setSaveError] = useState('')
  const [serverHasCluster, setServerHasCluster] = useState(false)
  const [machines, setMachines] = useState<Machine[]>([])
  const [assigningNode, setAssigningNode] = useState<NodeConfig | undefined>()
  const [deployOpen, setDeployOpen] = useState(false)
  const [deployNonce, setDeployNonce] = useState(0)

  const refreshMachines = () => {
    listMachines()
      .then(setMachines)
      .catch(() => setMachines([]))
  }
  useEffect(refreshMachines, [])

  // Hydrate from the server when there is no local draft (e.g. another
  // browser saved this cluster).
  useEffect(() => {
    if (!routeId) return
    let cancelled = false
    getCluster(routeId)
      .then((detail) => {
        if (cancelled || !detail) return
        setServerHasCluster(true)
        const known = loadClustersInfo().some(
          (i) => shortId(i.id) === routeId,
        )
        if (!known) {
          writeClusterDraft(detail)
          window.location.reload()
        }
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [routeId])

  const problems = useMemo(
    () => (config ? validateCluster(config, nodes) : []),
    [config, nodes],
  )
  const hasErrors = problems.some((p) => p.level === 'error')

  if (!info || !config) {
    return (
      <div className="flex flex-col items-start gap-y-4 p-8">
        <h1 className="primary-h3">Cluster not found</h1>
        <p className="primary-body3">
          This cluster has no local draft in this browser
          {serverHasCluster ? ' (loading it from the server…)' : ''}.
        </p>
        <CosButton type="secondary" onClick={() => navigate('/')}>
          Back to clusters
        </CosButton>
      </div>
    )
  }

  const serverByHostname: Record<string, Machine> = {}
  for (const m of machines) {
    if (m.assignment && m.assignment.clusterId === shortId(info.id)) {
      serverByHostname[m.assignment.hostname] = m
    }
  }
  const allAssigned =
    nodes.length > 0 && nodes.every((n) => !!serverByHostname[n.hostname])

  const detail: ClusterDetail = {
    clusterInfo: info,
    clusterConfig: config,
    nodeData: nodes,
  }
  const sid = shortId(info.id)

  const handleSave = async () => {
    setSaveState('saving')
    setSaveError('')
    try {
      await saveCluster(detail)
      setSaveState('saved')
      setServerHasCluster(true)
    } catch (err) {
      setSaveState('error')
      setSaveError(err instanceof Error ? err.message : String(err))
    }
  }

  const handleDeleteCluster = async () => {
    try {
      await deleteCluster(sid)
    } catch {
      // Absent on the server is fine; still drop the local draft.
    }
    removeClusterDraft(info.id)
    setClustersInfo(clustersInfo.filter((i) => i.id !== info.id))
    navigate('/')
  }

  const touch = () => setSaveState('unsaved')

  return (
    <div className="flex flex-col gap-y-6 p-8">
      <ClusterDetailCard
        info={info}
        config={config}
        onEdit={() => setClusterWizardOpen(true)}
      />

      <DeployProgress clusterId={sid} reloadSignal={deployNonce} />

      <ProblemBanner problems={problems} />
      {saveState === 'error' && (
        <ProblemBanner
          problems={[
            { level: 'error', title: 'Save failed', text: saveError },
          ]}
        />
      )}

      <div className="flex flex-wrap items-center gap-x-3">
        <CosButton
          onClick={() => {
            setEditingNode(undefined)
            setNodeWizardOpen(true)
          }}
        >
          Add node
        </CosButton>
        <CosButton
          type="secondary"
          disabled={hasErrors || nodes.length === 0 || saveState === 'saving'}
          loading={saveState === 'saving'}
          onClick={handleSave}
        >
          {saveState === 'saved' ? 'Saved' : 'Save to server'}
        </CosButton>
        {saveState === 'saved' && (
          <>
            <a href={clusterZipUrl(sid)} download>
              <CosButton type="secondary">Download cluster zip</CosButton>
            </a>
            <CosButton type="secondary" onClick={() => setUrlModalOpen(true)}>
              Get snapshot URLs
            </CosButton>
          </>
        )}
        <CosButton
          disabled={!allAssigned}
          onClick={() => setDeployOpen(true)}
        >
          {allAssigned
            ? 'Deploy to cluster'
            : 'Deploy (assign all servers first)'}
        </CosButton>
        <div className="flex-1" />
        <CosButton type="warning" onClick={() => setDeleteClusterOpen(true)}>
          Delete cluster
        </CosButton>
      </div>

      <NodeTable
        nodes={nodes}
        serverByHostname={serverByHostname}
        snapshotUrlFor={
          saveState === 'saved' || serverHasCluster
            ? (hostname) => nodeSnapshotUrl(sid, hostname)
            : undefined
        }
        onEdit={(node) => {
          setEditingNode(node)
          setNodeWizardOpen(true)
        }}
        onDuplicate={(node) => {
          let copy = 1
          const base = node.hostname
          while (nodes.some((n) => n.hostname === `${base}-${copy}`)) copy++
          setNodes([
            ...nodes,
            { ...node, id: newId(), hostname: `${base}-${copy}` },
          ])
          touch()
        }}
        onDelete={(node) => setDeletingNode(node)}
        onAssignServer={(node) => setAssigningNode(node)}
      />

      {assigningNode && (
        <AssignServerFlow
          isOpen
          node={assigningNode}
          machines={machines}
          currentMachineId={
            serverByHostname[assigningNode.hostname]?.id ?? undefined
          }
          onCancel={() => setAssigningNode(undefined)}
          onFinish={async ({ machineId, osDisk, node: updated }) => {
            try {
              await assignMachine(machineId, sid, updated.hostname, osDisk)
              // Rewrite the node's topology/roles to match the assigned box.
              setNodes(
                nodes.map((n) => (n.id === updated.id ? updated : n)),
              )
              touch()
              refreshMachines()
            } catch (e) {
              setSaveError(e instanceof Error ? e.message : String(e))
              setSaveState('error')
            }
            setAssigningNode(undefined)
          }}
        />
      )}

      <ClusterWizard
        isOpen={clusterWizardOpen}
        initialInfo={info}
        initialConfig={config}
        newClusterId={info.id}
        onCancel={() => setClusterWizardOpen(false)}
        onFinish={(newInfo, newConfig) => {
          setClustersInfo(
            clustersInfo.map((i) => (i.id === info.id ? newInfo : i)),
          )
          setConfig(newConfig)
          setClusterWizardOpen(false)
          touch()
        }}
      />

      <NodeWizard
        isOpen={nodeWizardOpen}
        initial={editingNode}
        takenHostnames={nodes
          .filter((n) => n.id !== editingNode?.id)
          .map((n) => n.hostname)}
        onCancel={() => setNodeWizardOpen(false)}
        onFinish={(node) => {
          if (editingNode) {
            setNodes(nodes.map((n) => (n.id === node.id ? node : n)))
          } else {
            setNodes([...nodes, node])
          }
          setNodeWizardOpen(false)
          touch()
        }}
      />

      {deletingNode && (
        <CosModal
          isOpen
          size="sm"
          title={`Delete node ${deletingNode.hostname}?`}
          actionText="Delete"
          onActionClick={() => {
            setNodes(nodes.filter((n) => n.id !== deletingNode.id))
            setDeletingNode(undefined)
            touch()
          }}
          onCloseClick={() => setDeletingNode(undefined)}
        >
          <p className="primary-body3">
            The node is removed from this cluster draft. Save to apply the
            change on the server.
          </p>
        </CosModal>
      )}

      {deleteClusterOpen && (
        <CosModal
          isOpen
          size="sm"
          title={`Delete cluster ${info.name}?`}
          actionText="Delete"
          onActionClick={handleDeleteCluster}
          onCloseClick={() => setDeleteClusterOpen(false)}
        >
          <p className="primary-body3">
            Removes the local draft and the generated snapshots on the server.
          </p>
        </CosModal>
      )}

      <SnapshotUrlModal
        isOpen={urlModalOpen}
        hostnames={nodes.map((n) => n.hostname)}
        onClose={() => setUrlModalOpen(false)}
      />

      <DeployModal
        isOpen={deployOpen}
        clusterId={sid}
        onCancel={() => setDeployOpen(false)}
        onStarted={() => {
          setDeployOpen(false)
          setDeployNonce((n) => n + 1)
        }}
      />
    </div>
  )
}
