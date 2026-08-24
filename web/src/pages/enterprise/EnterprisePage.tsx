import { CosButton, CosTag } from '@cube-frontend/ui-library'
import { useEffect, useState } from 'react'
import { listClusters } from '../../api/client'
import { Install, InstallState, listInstalls, Module } from '../../api/enterprise'
import { ClusterDigest } from '../../model/types'
import cubeAiAdvisorLogo from '../../assets/cube-ai-advisor-logo.svg'
import cubecmpLogo from '../../assets/cubecmp-logo.svg'
import { InstallModal } from './InstallModal'
import { UninstallModal } from './UninstallModal'

// logo (when set) is shown in place of the title text on the module card.
type ModuleCard = {
  module: Module
  title: string
  description: string
  logo?: string
}

const MODULES: ModuleCard[] = [
  {
    module: 'appfw',
    title: 'App-Framework',
    description: 'Install the App-Framework platform onto a running cluster.',
  },
  {
    module: 'cmp',
    title: 'CubeCMP',
    logo: cubecmpLogo,
    description:
      "Install CubeCMP (installs App-Framework first if the cluster doesn't have it).",
  },
  {
    module: 'advisor',
    title: 'Cube AI Advisor',
    logo: cubeAiAdvisorLogo,
    description:
      "Install Cube AI Advisor (installs App-Framework first if the cluster doesn't have it).",
  },
]

const MODULE_LABEL: Record<string, string> = {
  appfw: 'App-Framework',
  cmp: 'CubeCMP',
  advisor: 'Cube AI Advisor',
}

// Label a run with its framework/project — "CubeCMP@appfw", "App-Framework@appfw"
// — so runs on different frameworks are distinguishable at a glance.
const runLabel = (inst: Install): string => {
  const base = MODULE_LABEL[inst.Module] ?? inst.Module
  const fw = inst.Params?.Framework || inst.Params?.Project
  return fw ? `${base}@${fw}` : base
}

const STATE_TAG: Record<
  InstallState,
  { color: 'default' | 'primary-blue' | 'cyan' | 'dark'; variant: 'filled' | 'stroke' }
> = {
  running: { color: 'primary-blue', variant: 'filled' },
  done: { color: 'cyan', variant: 'stroke' },
  error: { color: 'dark', variant: 'filled' },
  cancelled: { color: 'default', variant: 'stroke' },
}

// Target the modal opens against: a module (and optionally a cluster to reattach).
type OpenTarget = { module: Module; clusterId?: string }

// progressLabel summarises where a run is, e.g. "4/7 · Import Rancher image".
const progressLabel = (inst: Install): string => {
  const total = inst.Steps.length
  const cur = inst.Steps[inst.Current] ?? inst.Steps[total - 1]
  const n = Math.min(inst.Current + 1, total)
  return `${n}/${total} · ${cur?.Title || cur?.Name || ''}`
}

export const EnterprisePage = () => {
  const [selected, setSelected] = useState<OpenTarget | null>(null)
  const [uninstalling, setUninstalling] = useState<OpenTarget | null>(null)
  const [installs, setInstalls] = useState<Install[]>([])
  const [clusterNames, setClusterNames] = useState<Record<string, string>>({})

  useEffect(() => {
    listClusters()
      .then((cs: ClusterDigest[]) =>
        setClusterNames(Object.fromEntries(cs.map((c) => [c.id, c.name]))),
      )
      .catch(() => {})
  }, [])

  // Poll the install list so the dashboard tracks every cluster's run live.
  useEffect(() => {
    const load = () =>
      listInstalls()
        .then(setInstalls)
        .catch(() => {})
    load()
    const t = setInterval(load, 3000)
    return () => clearInterval(t)
  }, [])

  const running = installs.filter((i) => i.State === 'running')
  // A completed uninstall is a teardown — drop it from the list once done (the
  // module is gone; nothing to reattach to). Running uninstalls still show.
  const recent = installs.filter(
    (i) => i.State !== 'running' && i.Op !== 'uninstall',
  )

  const Row = (inst: Install) => (
    <button
      key={`${inst.ClusterID}/${inst.Module}`}
      type="button"
      onClick={() =>
        setSelected({ module: inst.Module as Module, clusterId: inst.ClusterID })
      }
      className="flex items-center gap-x-3 rounded-lg border border-functional-border-divider px-4 py-3 text-left hover:border-primary hover:bg-functional-hover-grey"
    >
      <CosTag
        variant={STATE_TAG[inst.State].variant}
        color={STATE_TAG[inst.State].color}
      >
        {inst.State}
      </CosTag>
      <span className="primary-body4 font-semibold">
        {clusterNames[inst.ClusterID] ?? inst.ClusterID}
      </span>
      <span className="secondary-body5 text-functional-text-light">
        {runLabel(inst)}
      </span>
      <div className="flex-1" />
      <span className="secondary-body5 font-mono text-functional-text-light">
        {progressLabel(inst)}
      </span>
    </button>
  )

  return (
    <div className="flex flex-col gap-y-6 p-8">
      <div className="flex flex-col gap-y-2">
        <h1 className="primary-h2">Enterprise modules</h1>
        <p className="primary-body2">
          Install enterprise modules onto a running cluster.
        </p>
      </div>

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        {MODULES.map((m) => (
          <div
            key={m.module}
            className="flex flex-col gap-y-2 rounded-xl border border-functional-border-divider p-4"
          >
            {m.logo ? (
              <img src={m.logo} alt={m.title} className="h-8 self-start" />
            ) : (
              <h3 className="primary-body2 font-semibold">{m.title}</h3>
            )}
            <p className="secondary-body4 text-functional-text-light">
              {m.description}
            </p>
            <div className="flex gap-x-2 pt-2">
              <CosButton onClick={() => setSelected({ module: m.module })}>
                Install
              </CosButton>
              <CosButton
                type="warning"
                onClick={() => setUninstalling({ module: m.module })}
              >
                Uninstall
              </CosButton>
            </div>
          </div>
        ))}
      </div>

      {installs.length > 0 && (
        <div className="flex flex-col gap-y-3">
          <h2 className="primary-body1 font-semibold">
            Installs in progress
            {running.length > 0 && (
              <span className="secondary-body5 ml-2 text-functional-text-light">
                {running.length} running
              </span>
            )}
          </h2>
          <div className="flex flex-col gap-y-2">
            {running.map(Row)}
            {recent.map(Row)}
          </div>
        </div>
      )}

      {selected && (
        <InstallModal
          module={selected.module}
          initialClusterId={selected.clusterId}
          onClose={() => setSelected(null)}
        />
      )}

      {uninstalling && (
        <UninstallModal
          module={uninstalling.module}
          initialClusterId={uninstalling.clusterId}
          onClose={() => setUninstalling(null)}
        />
      )}
    </div>
  )
}
