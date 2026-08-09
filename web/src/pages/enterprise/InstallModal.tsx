// Enterprise-module install form + progress hand-off. Mirrors DeployModal's
// CosModal shell; params match the Go InstallParams struct (PascalCase on
// the wire, see api/enterprise.ts).
import { CosInlineNotification, CosModal } from '@cube-frontend/ui-library'
import { useEffect, useState } from 'react'
import { getCluster, listClusters } from '../../api/client'
import {
  Artifacts,
  clusterInfo,
  getArtifacts,
  getInstall,
  Install,
  InstallParams,
  Module,
  startInstall,
  StartInstallBody,
} from '../../api/enterprise'
import { allIFs, ClusterDetail, ClusterDigest } from '../../model/types'
import { InstallProgress } from './InstallProgress'

export type InstallModalProps = {
  module: Module
  onClose: () => void
  // Preselect a cluster (used when opening from the dashboard to reattach to an
  // in-flight run).
  initialClusterId?: string
}

const TITLES: Record<Module, string> = {
  appfw: 'Install App-Framework',
  cmp: 'Install CubeCMP',
}

const Field = (props: {
  label: string
  value: string
  placeholder?: string
  type?: string
  required?: boolean
  disabled?: boolean
  // options render a datalist for free-text-with-suggestions (combo input).
  options?: string[]
  // tabFill: pressing Tab in the empty field accepts this default value.
  tabFill?: string
  onChange: (v: string) => void
}) => {
  const listId = props.options
    ? `${props.label.replace(/\s+/g, '-').toLowerCase()}-options`
    : undefined
  return (
    <label className="flex flex-col gap-y-1">
      <span className="secondary-body5 font-medium text-functional-text-secondary">
        {props.label}
      </span>
      <input
        type={props.type ?? 'text'}
        required={props.required}
        disabled={props.disabled}
        list={listId}
        className="primary-body4 rounded-md border border-functional-border-divider px-3 py-2 font-mono outline-none focus:border-primary disabled:cursor-not-allowed disabled:bg-functional-hover-grey disabled:text-functional-text-light"
        value={props.value}
        placeholder={props.placeholder}
        onKeyDown={(e) => {
          if (
            e.key === 'Tab' &&
            !e.shiftKey &&
            props.tabFill &&
            props.value === ''
          ) {
            props.onChange(props.tabFill)
          }
        }}
        onChange={(e) => props.onChange(e.target.value)}
      />
      {listId && (
        <datalist id={listId}>
          {props.options?.map((o) => (
            <option key={o} value={o} />
          ))}
        </datalist>
      )}
    </label>
  )
}

const Select = (props: {
  label: string
  value: string
  options: string[]
  placeholder: string
  disabled?: boolean
  onChange: (v: string) => void
}) => (
  <label className="flex flex-col gap-y-1">
    <span className="secondary-body5 font-medium text-functional-text-secondary">
      {props.label}
    </span>
    <select
      disabled={props.disabled}
      className="primary-body4 rounded-md border border-functional-border-divider px-3 py-2 outline-none focus:border-primary disabled:cursor-not-allowed disabled:bg-functional-hover-grey disabled:text-functional-text-light"
      value={props.value}
      onChange={(e) => props.onChange(e.target.value)}
    >
      <option value="">{props.placeholder}</option>
      {props.options.map((o) => (
        <option key={o} value={o}>
          {o}
        </option>
      ))}
    </select>
  </label>
)

// Cube@<last-two-octets> — same default the backend applies when the
// password is left blank.
const defaultPassword = (host: string): string => {
  const parts = host.split('.')
  return parts.length >= 4 ? `Cube@${parts.slice(2).join('.')}` : ''
}

// connectHost is the IP the driver reaches the cluster on (and derives the
// default password from): the VIP for HA, else the single node's mgmt IP.
const connectHost = (d: ClusterDetail): string => {
  const vip = d.clusterConfig.HASettings.virtualIP
  if (vip) return vip
  for (const n of d.nodeData) {
    const mgmt = allIFs(n).find(
      (f) => f.id === n.roleSettings.mgmtIF.id && f.IPAddr,
    )
    if (mgmt?.IPAddr) return mgmt.IPAddr
  }
  return ''
}

export function InstallModal({
  module,
  onClose,
  initialClusterId,
}: InstallModalProps) {
  const [clusters, setClusters] = useState<ClusterDigest[]>([])
  const [clusterId, setClusterId] = useState(initialClusterId ?? '')
  const [host, setHost] = useState('')
  const [artifacts, setArtifacts] = useState<Artifacts>({ AppFW: [], CMP: [] })
  const [networkOpts, setNetworkOpts] = useState<string[]>([])
  const [infoErr, setInfoErr] = useState('')
  const [infoLoading, setInfoLoading] = useState(false)
  const [airgapSupported, setAirgapSupported] = useState(true)
  const [suggestedLbIp, setSuggestedLbIp] = useState('')
  const [suggestedStorage, setSuggestedStorage] = useState('')
  const [version, setVersion] = useState('')
  const [manifestOpts, setManifestOpts] = useState<string[]>([])
  const [manifest, setManifest] = useState('')

  const [password, setPassword] = useState('')
  // framework is the app-framework name (framework_create FRAMEWORK_NAME, and
  // the app_register target for CMP). The image import always uses the admin
  // Keystone tenant, so there is no separate "project" field.
  const [framework, setFramework] = useState('appfw')
  const [publicNet, setPublicNet] = useState('public')
  const [mgmtNet, setMgmtNet] = useState('public')
  const [lbIp, setLbIp] = useState('')
  const [osImage, setOsImage] = useState('')
  const [pigz, setPigz] = useState('')
  const [manual, setManual] = useState(false)
  const [airgap, setAirgap] = useState(false)

  const [error, setError] = useState('')
  const [starting, setStarting] = useState(false)
  const [started, setStarted] = useState<Install | null>(null)

  useEffect(() => {
    listClusters()
      .then(setClusters)
      .catch(() => setClusters([]))
    getArtifacts()
      .then(setArtifacts)
      .catch(() => setArtifacts({ AppFW: [], CMP: [] }))
  }, [])

  // Stop Escape from dismissing the modal (CosModal closes on Escape) — an
  // install form/progress shouldn't vanish on a stray keypress. Capture-phase
  // so it runs before CosModal's window listener; the browser default (closing
  // a focused dropdown) still works since we don't preventDefault.
  useEffect(() => {
    const stopEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') e.stopImmediatePropagation()
    }
    window.addEventListener('keydown', stopEsc, true)
    return () => window.removeEventListener('keydown', stopEsc, true)
  }, [])

  useEffect(() => {
    if (!clusterId) {
      setHost('')
      return
    }
    getCluster(clusterId)
      .then((c) => setHost(c ? connectHost(c) : ''))
      .catch(() => setHost(''))
  }, [clusterId])

  // Re-attach to an existing run for this cluster+module. The run lives
  // server-side, so reopening the modal jumps back to it. Opened from the
  // dashboard (initialClusterId matches) we show any run — running or its final
  // result; picking a cluster manually reattaches only to a live run so a stale
  // terminal record doesn't block starting a fresh install.
  useEffect(() => {
    if (!clusterId || started) return
    const fromDashboard = clusterId === initialClusterId
    getInstall(clusterId, module)
      .then((inst) => {
        if (inst.State === 'running' || fromDashboard) setStarted(inst)
      })
      .catch(() => {}) // 404 = nothing in flight; show the form
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clusterId, module])

  // Live-query the selected cluster's OpenStack for its networks (and air-gap
  // support + a suggested LB IP) to populate the form.
  useEffect(() => {
    if (!clusterId) {
      setNetworkOpts([])
      setInfoErr('')
      setInfoLoading(false)
      setSuggestedLbIp('')
      setSuggestedStorage('')
      setVersion('')
      setManifestOpts([])
      setManifest('')
      return
    }
    setInfoErr('')
    setInfoLoading(true)
    clusterInfo(clusterId, password)
      .then((info) => {
        setNetworkOpts(info.networks)
        const pub = info.networks.includes('public') ? 'public' : ''
        setPublicNet(pub)
        setMgmtNet(pub)
        setAirgapSupported(info.airgapSupported)
        if (!info.airgapSupported) setAirgap(false)
        setSuggestedLbIp(info.suggestedLBIP)
        setSuggestedStorage(info.suggestedStorage)
        setVersion(info.version)
        setManifestOpts(info.manifests)
        // Auto-select the manifest matched to the detected version; operator
        // can override.
        setManifest(info.manifest)
      })
      .catch((e) => {
        setNetworkOpts([])
        setInfoErr(`Could not read the cluster's networks: ${e}`)
      })
      .finally(() => setInfoLoading(false))
    // password intentionally excluded: fetch on cluster change, using the
    // password entered at that point (blank → server default).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clusterId])

  const osImages = artifacts.AppFW.filter((n) => n.endsWith('.raw'))
  const fsImage = artifacts.AppFW.find((n) => /manila-.*\.qcow2$/.test(n)) ?? ''
  const lbImage =
    artifacts.AppFW.find((n) => /amphora-.*\.qcow2$/.test(n)) ?? ''

  const run = async () => {
    setStarting(true)
    setError('')
    try {
      const params: InstallParams = {
        Project: framework,
        PublicNet: publicNet,
        MgmtNet: mgmtNet,
        LBIP: lbIp,
        OSImage: osImage,
        Framework: module === 'cmp' ? framework : '',
        AppFile: module === 'cmp' ? pigz : '',
        FsImage: fsImage,
        LBImage: lbImage,
        StorageBackend: suggestedStorage,
      }
      const body: StartInstallBody = {
        module,
        params,
        manual,
        simulateAirgap: airgap,
        password,
        manifest,
      }
      const install = await startInstall(clusterId, body)
      setStarted(install)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setStarting(false)
    }
  }

  return (
    <CosModal
      isOpen
      title={TITLES[module]}
      size="md"
      actionText="Install"
      isActionButtonVisible={!started}
      isCancelButtonVisible={!started}
      actionButtonProps={{
        disabled:
          !clusterId ||
          infoLoading ||
          !framework ||
          !lbIp ||
          !osImage ||
          (module === 'cmp' && !pigz) ||
          starting,
        loading: starting || infoLoading,
      }}
      onActionClick={run}
      // The X (and backdrop) close the modal; a running install keeps going
      // server-side and resumes on reopen (reattach) or from the dashboard.
      // Escape stays neutralized (capture-phase effect) to avoid a stray keypress
      // dismissing the form/progress view.
      onCloseClick={onClose}
    >
      {started ? (
        <InstallProgress
          clusterId={clusterId}
          module={module}
          install={started}
          onClose={onClose}
        />
      ) : (
        <div className="flex flex-col gap-y-4">
          <label className="flex flex-col gap-y-1">
            <span className="secondary-body5 font-medium text-functional-text-secondary">
              Cluster
            </span>
            <select
              className="primary-body4 rounded-md border border-functional-border-divider px-3 py-2 outline-none focus:border-primary"
              value={clusterId}
              onChange={(e) => setClusterId(e.target.value)}
            >
              <option value="">Select a cluster…</option>
              {clusters.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </label>

          <Field
            label="Password"
            type="password"
            value={password}
            placeholder={defaultPassword(host) || 'Cube@<last 2 octets>'}
            tabFill={defaultPassword(host)}
            onChange={setPassword}
          />

          {infoErr && (
            <CosInlineNotification
              type="warning"
              title="Cluster query failed"
              isClosable={false}
            >
              {infoErr}
            </CosInlineNotification>
          )}
          {infoLoading && (
            <span className="secondary-body5 flex items-center gap-x-2 text-functional-text-light">
              <span className="h-3 w-3 animate-spin rounded-full border-2 border-functional-border-divider border-t-primary" />
              Reading the cluster&apos;s networks…
            </span>
          )}
          {manifestOpts.length > 0 && (
            <label className="flex flex-col gap-y-1">
              <span className="secondary-body5 font-medium text-functional-text-secondary">
                Module set (CubeCOS version)
              </span>
              <select
                disabled={infoLoading}
                className="primary-body4 rounded-md border border-functional-border-divider px-3 py-2 outline-none focus:border-primary disabled:cursor-not-allowed disabled:bg-functional-hover-grey"
                value={manifest}
                onChange={(e) => setManifest(e.target.value)}
              >
                <option value="">Auto / none</option>
                {manifestOpts.map((o) => (
                  <option key={o} value={o}>
                    {o}
                  </option>
                ))}
              </select>
              {version && (
                <span className="secondary-body6 text-functional-text-light">
                  Detected cluster version {version}
                  {manifest ? ` → ${manifest}` : ' (no exact match)'}
                </span>
              )}
            </label>
          )}
          <Field
            label="Framework"
            value={framework}
            required
            placeholder="e.g. appfw"
            onChange={setFramework}
          />
          <Select
            label="Public net"
            value={publicNet}
            options={networkOpts}
            placeholder={infoLoading ? 'Loading…' : 'Select the public network…'}
            disabled={infoLoading || networkOpts.length === 0}
            onChange={setPublicNet}
          />
          <Select
            label="Mgmt net"
            value={mgmtNet}
            options={networkOpts}
            placeholder={
              infoLoading ? 'Loading…' : 'Select the management network…'
            }
            disabled={infoLoading || networkOpts.length === 0}
            onChange={setMgmtNet}
          />
          <Field
            label="LB IP"
            value={lbIp}
            placeholder={suggestedLbIp || 'e.g. 10.32.1.120'}
            tabFill={suggestedLbIp}
            onChange={setLbIp}
          />
          <Select
            label="OS image"
            value={osImage}
            options={osImages}
            placeholder="Select the rancher image…"
            onChange={setOsImage}
          />

          {module === 'cmp' && (
            <>
              <Select
                label=".pigz package"
                value={pigz}
                options={artifacts.CMP}
                placeholder="Select the CubeCMP package…"
                onChange={setPigz}
              />
            </>
          )}

          <label className="flex items-start gap-x-2 rounded-md border border-functional-border-divider px-3 py-2">
            <input
              type="checkbox"
              className="mt-1"
              checked={manual}
              onChange={(e) => setManual(e.target.checked)}
            />
            <span>
              <span className="primary-body4 font-semibold">
                Manual (step-by-step)
              </span>
              <span className="secondary-body5 block text-functional-text-light">
                Advance each install step yourself instead of running them
                automatically.
              </span>
            </span>
          </label>

          <details className="rounded-md border border-functional-border-divider p-3">
            <summary className="primary-body4 cursor-pointer font-semibold">
              Advanced
            </summary>
            <div className="mt-3 flex flex-col gap-y-3">
              <label className="flex items-start gap-x-2">
                <input
                  type="checkbox"
                  className="mt-1"
                  checked={airgap}
                  disabled={!airgapSupported}
                  onChange={(e) => setAirgap(e.target.checked)}
                />
                <span>
                  <span className="primary-body4 font-semibold">
                    Air-gap simulation
                  </span>
                  <span className="secondary-body5 block text-functional-text-light">
                    {airgapSupported ? (
                      <>
                        Enforces an air-gapped install during this run. Cleared
                        afterward with{' '}
                        <span className="font-mono">
                          cubectl node exec -p &apos;hex_sdk
                          airgap_sim_clear&apos;
                        </span>
                        .
                      </>
                    ) : (
                      'Not supported on this cluster version.'
                    )}
                  </span>
                </span>
              </label>
            </div>
          </details>

          {error && (
            <CosInlineNotification type="error" title="Error" isClosable={false}>
              {error}
            </CosInlineNotification>
          )}
        </div>
      )}
    </CosModal>
  )
}
