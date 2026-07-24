import { CosButton, CosModal } from '@cube-frontend/ui-library'
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { deleteCluster, listClusters } from '../../api/client'
import { ClusterWizard } from '../../components/wizards/cluster/ClusterWizard'
import {
  ClusterDetail,
  ClusterDigest,
  ClusterInfo,
  NodeConfig,
  shortId,
} from '../../model/types'
import {
  loadClusterConfig,
  loadNodes,
  removeClusterDraft,
  useClustersInfo,
  writeClusterDraft,
} from '../../state/clusters'
import { newId } from '../../utils/random'
import { ClusterCard } from './ClusterCard'
import { ImportModal } from './ImportModal'

const HowTo = () => (
  <div className="flex flex-col gap-y-3">
    <h2 className="primary-h4">How to use this tool</h2>
    <ol className="primary-body3 list-inside list-decimal space-y-1">
      <li>
        Click <strong>Create cluster</strong> and follow the wizard for the
        cluster-wide settings.
      </li>
      <li>
        Open the cluster and <strong>Add node</strong> for each machine
        (network interfaces + role).
      </li>
      <li>
        <strong>Save to server</strong> to generate the snapshots, then
        download the zip or use <strong>Get snapshot URLs</strong>.
      </li>
      <li>
        On a freshly PXE re-imaged node, run{' '}
        <code>snapshot pull url &lt;hostname&gt;</code> and{' '}
        <code>snapshot apply</code> from the CLI to skip step-by-step
        configuration.
      </li>
    </ol>
  </div>
)

export const LandingPage = () => {
  const navigate = useNavigate()
  const { clustersInfo, setClustersInfo } = useClustersInfo()
  const [wizardOpen, setWizardOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [deleting, setDeleting] = useState<ClusterInfo | null>(null)
  const [serverClusters, setServerClusters] = useState<ClusterDigest[]>([])
  const [newClusterId] = useState(newId)

  useEffect(() => {
    listClusters()
      .then(setServerClusters)
      .catch(() => setServerClusters([]))
  }, [])

  const localShortIds = new Set(clustersInfo.map((i) => shortId(i.id)))
  const serverOnly = serverClusters.filter((c) => !localShortIds.has(c.id))

  const exportCluster = (info: ClusterInfo) => {
    const detail: ClusterDetail = {
      clusterInfo: info,
      clusterConfig: loadClusterConfig(info.id) ?? {
        DNS: [],
        timezone: { name: '', offset: 0 },
        roleSettings: { extIP: '', region: '', secretSeed: '', mgmtCIDR: '' },
        HA: false,
        HASettings: {},
      },
      nodeData: loadNodes(info.id),
    }
    const blob = new Blob([JSON.stringify(detail, null, 4)], {
      type: 'application/json',
    })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'clusterDetail.json'
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="flex flex-col gap-y-8 p-8">
      <div className="flex flex-col gap-y-3">
        <h1 className="primary-h2">Welcome to Cube Snapshot Generator</h1>
        <p className="primary-body2">
          Generate CubeCOS cluster snapshots — per-node configuration bundles a
          freshly imaged node applies in one step instead of walking through
          first-time setup.
        </p>
      </div>

      <HowTo />

      <div className="flex items-center gap-x-3">
        <h2 className="primary-h4">
          Clusters ({clustersInfo.length + serverOnly.length})
        </h2>
        <CosButton size="sm" onClick={() => setWizardOpen(true)}>
          Create cluster
        </CosButton>
        <CosButton
          size="sm"
          type="secondary"
          onClick={() => setImportOpen(true)}
        >
          Import
        </CosButton>
      </div>

      <div className="flex flex-wrap gap-4">
        {clustersInfo.map((info, index) => {
          const nodes: NodeConfig[] = loadNodes(info.id)
          return (
            <ClusterCard
              key={info.id}
              name={info.name}
              nodes={nodes}
              fallbackName={`Cluster ${index + 1}`}
              onOpen={() => navigate(`/clusters/${shortId(info.id)}`)}
              onRename={(name) =>
                setClustersInfo(
                  clustersInfo.map((i) =>
                    i.id === info.id ? { ...i, name } : i,
                  ),
                )
              }
              onExport={() => exportCluster(info)}
              onDelete={() => setDeleting(info)}
            />
          )
        })}
        {serverOnly.map((digest) => (
          <ClusterCard
            key={digest.id}
            name={digest.name}
            nodes={[]}
            serverOnly
            fallbackName={digest.name}
            onOpen={() => navigate(`/clusters/${digest.id}`)}
            onDelete={() => {
              void deleteCluster(digest.id).then(() =>
                setServerClusters(
                  serverClusters.filter((c) => c.id !== digest.id),
                ),
              )
            }}
          />
        ))}
      </div>

      <ClusterWizard
        isOpen={wizardOpen}
        newClusterId={newClusterId}
        onCancel={() => setWizardOpen(false)}
        onFinish={(info, config) => {
          writeClusterDraft({
            clusterInfo: info,
            clusterConfig: config,
            nodeData: [],
          })
          setWizardOpen(false)
          navigate(`/clusters/${shortId(info.id)}`)
        }}
      />

      <ImportModal
        isOpen={importOpen}
        onCancel={() => setImportOpen(false)}
        onImport={(detail) => {
          writeClusterDraft(detail)
          setImportOpen(false)
          navigate(`/clusters/${shortId(detail.clusterInfo.id)}`)
        }}
      />

      {deleting && (
        <CosModal
          isOpen
          size="sm"
          title={`Delete cluster ${deleting.name}?`}
          actionText="Delete"
          onActionClick={() => {
            void deleteCluster(shortId(deleting.id)).catch(() => {})
            removeClusterDraft(deleting.id)
            setClustersInfo(clustersInfo.filter((i) => i.id !== deleting.id))
            setDeleting(null)
          }}
          onCloseClick={() => setDeleting(null)}
        >
          <p className="primary-body3">
            Removes the local draft and any generated snapshots on the server.
          </p>
        </CosModal>
      )}
    </div>
  )
}
