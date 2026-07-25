import { CosButton, CosModal } from '@cube-frontend/ui-library'
import { ReactNode, useEffect, useState } from 'react'
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

const ACCENT = '#4C68F9' // cube primary

const Code = ({ children }: { children: ReactNode }) => (
  <code className="rounded border border-functional-border-divider bg-grey-0 px-1 font-mono text-[0.85em]">
    {children}
  </code>
)

const STEPS: { title: string; body: ReactNode }[] = [
  {
    title: 'Create a cluster',
    body: 'Follow the wizard for the cluster-wide settings — DNS, timezone, roles, and HA.',
  },
  {
    title: 'Add its nodes',
    body: 'Add each machine with its network interfaces and role.',
  },
  {
    title: 'Generate snapshots',
    body: (
      <>
        <strong>Save to server</strong>, then download the zip or copy the
        per-node snapshot URLs.
      </>
    ),
  },
  {
    title: 'Apply on the node',
    body: (
      <>
        On a freshly re-imaged node, run <Code>snapshot pull url &lt;hostname&gt;</Code>{' '}
        then <Code>snapshot apply</Code> — no step-by-step setup.
      </>
    ),
  },
]

const HowItWorks = () => (
  <div className="flex flex-col gap-y-4">
    <h2 className="primary-h4">How it works</h2>
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      {STEPS.map((s, i) => (
        <div
          key={i}
          className="flex flex-col gap-y-2 rounded-xl border border-functional-border-divider p-4"
        >
          <span
            className="primary-body4 flex h-8 w-8 items-center justify-center rounded-full font-semibold text-white"
            style={{ backgroundColor: ACCENT }}
          >
            {i + 1}
          </span>
          <h3 className="primary-body2 font-semibold">{s.title}</h3>
          <p className="secondary-body4 text-functional-text-light">{s.body}</p>
        </div>
      ))}
    </div>
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
  const clusterCount = clustersInfo.length + serverOnly.length

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
      {/* Hero */}
      <div className="relative overflow-hidden rounded-2xl border border-functional-border-divider p-8">
        <div
          className="pointer-events-none absolute inset-x-0 top-0 h-1"
          style={{ background: ACCENT }}
        />
        <div className="flex max-w-2xl flex-col gap-y-4">
          <span
            className="secondary-body5 inline-flex w-fit items-center gap-x-2 rounded-full border px-3 py-1 font-semibold uppercase tracking-wide"
            style={{ color: ACCENT, borderColor: `${ACCENT}55` }}
          >
            <span
              className="h-1.5 w-1.5 rounded-full"
              style={{ backgroundColor: ACCENT }}
            />
            Zero-touch CubeCOS provisioning
          </span>
          <h1 className="primary-h2">Welcome to cubeDriver</h1>
          <p className="primary-body2 text-functional-text-secondary">
            Generate per-node CubeCOS snapshots — a freshly imaged node applies
            its entire configuration in one step, instead of being walked
            through first-time setup by hand.
          </p>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2 pt-1">
            <CosButton onClick={() => setWizardOpen(true)}>
              Create cluster
            </CosButton>
            <CosButton type="secondary" onClick={() => setImportOpen(true)}>
              Import cluster
            </CosButton>
          </div>
        </div>
      </div>

      <HowItWorks />

      {/* Clusters */}
      <div className="flex flex-col gap-y-4">
        <div className="flex items-center gap-x-3">
          <h2 className="primary-h4">Your clusters ({clusterCount})</h2>
          {clusterCount > 0 && (
            <CosButton size="sm" onClick={() => setWizardOpen(true)}>
              Create cluster
            </CosButton>
          )}
        </div>

        {clusterCount === 0 ? (
          <div className="flex flex-col items-start gap-y-3 rounded-xl border border-dashed border-functional-border-divider p-8">
            <p className="primary-body3 text-functional-text-light">
              No clusters yet. Create one to start generating snapshots.
            </p>
            <CosButton onClick={() => setWizardOpen(true)}>
              Create your first cluster
            </CosButton>
          </div>
        ) : (
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
        )}
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
