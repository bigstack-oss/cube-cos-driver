// Confirm + drive an enterprise-module uninstall. CMP removes the portal (App-
// Framework kept); App-Framework removes the framework and every app on it.
// Reuses InstallProgress for step progress (the uninstall run shares the install
// status endpoint, keyed by cluster+module).
import { CosInlineNotification, CosModal } from '@cube-frontend/ui-library'
import { useEffect, useState } from 'react'
import { listClusters } from '../../api/client'
import { Install, Module, startUninstall } from '../../api/enterprise'
import { ClusterDigest } from '../../model/types'
import {
  ClusterTargetPicker,
  emptyTarget,
  Target,
  targetId,
  targetVip,
} from './ClusterTarget'
import { InstallProgress } from './InstallProgress'

const TITLES: Record<Module, string> = {
  appfw: 'Uninstall App-Framework',
  cmp: 'Uninstall CubeCMP',
}

// Module-specific warning. App-Framework hosts multiple apps (CMP is one), so its
// warning is intentionally generic about "every app on it".
const WARNINGS: Record<Module, string> = {
  appfw:
    'This deletes the App-Framework and removes every app running on it (including CubeCMP, if installed). Imported service and Rancher images are kept.',
  cmp: 'This removes the CubeCMP portal. The App-Framework, its registry, and imported images are kept.',
}

export type UninstallModalProps = {
  module: Module
  initialClusterId?: string
  onClose: () => void
}

export function UninstallModal({
  module,
  initialClusterId,
  onClose,
}: UninstallModalProps) {
  const [clusters, setClusters] = useState<ClusterDigest[]>([])
  const [target, setTarget] = useState<Target>(emptyTarget(initialClusterId ?? ''))
  const [error, setError] = useState('')
  const [starting, setStarting] = useState(false)
  const [started, setStarted] = useState<Install | null>(null)
  const [confirming, setConfirming] = useState(false)

  useEffect(() => {
    listClusters()
      .then(setClusters)
      .catch(() => setClusters([]))
  }, [])

  // Neutralize Escape so a stray keypress can't dismiss a running uninstall.
  useEffect(() => {
    const stopEsc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') e.stopPropagation()
    }
    window.addEventListener('keydown', stopEsc, true)
    return () => window.removeEventListener('keydown', stopEsc, true)
  }, [])

  const run = async () => {
    const id = targetId(target)
    if (!id) return
    setStarting(true)
    setError('')
    try {
      const inst = await startUninstall(id, {
        module,
        params: { Project: 'appfw', Framework: 'appfw' },
        manual: false,
        vip: targetVip(target) || undefined,
      })
      setStarted(inst)
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
      actionText={confirming ? 'Yes, uninstall' : 'Uninstall'}
      isActionButtonVisible={!started}
      isCancelButtonVisible={!started}
      actionButtonProps={{
        disabled: !targetId(target) || starting,
        loading: starting,
      }}
      // First click asks for a final confirmation; the second runs it.
      onActionClick={confirming ? run : () => setConfirming(true)}
      onCloseClick={onClose}
    >
      {started ? (
        <InstallProgress
          clusterId={targetId(target)}
          module={module}
          install={started}
          onClose={onClose}
        />
      ) : (
        <div className="flex flex-col gap-y-4">
          <CosInlineNotification
            type="warning"
            title="This is destructive"
            isClosable={false}
          >
            {WARNINGS[module]}
          </CosInlineNotification>

          <ClusterTargetPicker
            clusters={clusters}
            value={target}
            onChange={(t) => {
              setTarget(t)
              setConfirming(false)
            }}
            disabled={confirming}
          />

          {confirming && (
            <CosInlineNotification
              type="error"
              title="Confirm uninstall"
              isClosable={false}
            >
              {module === 'appfw'
                ? `This permanently deletes the App-Framework on ${targetId(target)} and every app running on it. This cannot be undone. Click "Yes, uninstall" to proceed.`
                : `This permanently removes CubeCMP from ${targetId(target)}. This cannot be undone. Click "Yes, uninstall" to proceed.`}
            </CosInlineNotification>
          )}

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
