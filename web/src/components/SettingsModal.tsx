// Driver settings: point the driver at the enterprise images folder — the place
// that holds the App-Framework + CubeCMP install artifacts (the large rancher
// .raw, the qcow2 service images, the cube-portal .pigz). These are big and can
// live on separately-mounted media (USB / virtual media) that isn't shipped
// with the pxeserver deployment, so the folder is set here at runtime and kept
// apart from the cluster snapshot store.
import { CosInlineNotification, CosModal } from '@cube-frontend/ui-library'
import { useEffect, useState } from 'react'
import {
  BlockDevice,
  DirListing,
  getDevices,
  getEnterpriseDir,
  listDirs,
  mountDevice,
  setEnterpriseDir,
} from '../api/enterprise'

export type SettingsModalProps = {
  onClose: () => void
  // Called after a successful change so callers can refresh artifact lists.
  onChanged?: () => void
}

export function SettingsModal({ onClose, onChanged }: SettingsModalProps) {
  const [imageDir, setImageDir] = useState('')
  const [mounted, setMounted] = useState(false)
  const [appfwCount, setAppfwCount] = useState(0)
  const [cmpCount, setCmpCount] = useState(0)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [browsing, setBrowsing] = useState<DirListing | null>(null)
  const [browseErr, setBrowseErr] = useState('')
  const [devices, setDevices] = useState<BlockDevice[] | null>(null)
  const [deviceErr, setDeviceErr] = useState('')
  const [mountingDev, setMountingDev] = useState('')

  const openBrowse = (path: string) => {
    setBrowseErr('')
    listDirs(path || '/')
      .then(setBrowsing)
      .catch((e) => {
        // fall back to root if the current path can't be read
        if (path && path !== '/') openBrowse('/')
        else setBrowseErr(e instanceof Error ? e.message : String(e))
      })
  }

  const openDevices = () => {
    setDeviceErr('')
    getDevices()
      .then(setDevices)
      .catch((e) => setDeviceErr(e instanceof Error ? e.message : String(e)))
  }

  const doMount = async (dev: BlockDevice) => {
    if (dev.mountpoint) {
      setImageDir(dev.mountpoint)
      setSaved(false)
      openBrowse(dev.mountpoint)
      return
    }
    setMountingDev(dev.name)
    setDeviceErr('')
    try {
      const mp = await mountDevice(dev.name)
      openDevices() // refresh mount state
      setImageDir(mp)
      setSaved(false)
      openBrowse(mp)
    } catch (e) {
      setDeviceErr(e instanceof Error ? e.message : String(e))
    } finally {
      setMountingDev('')
    }
  }

  useEffect(() => {
    getEnterpriseDir()
      .then((r) => {
        setImageDir(r.imageDir)
        setMounted(r.mounted)
        setAppfwCount(r.appfwCount)
        setCmpCount(r.cmpCount)
      })
      .catch(() => {})
  }, [])

  const apply = async () => {
    setSaving(true)
    setError('')
    setSaved(false)
    try {
      const r = await setEnterpriseDir(imageDir.trim())
      setImageDir(r.imageDir)
      setMounted(r.mounted)
      setAppfwCount(r.appfwCount)
      setCmpCount(r.cmpCount)
      setSaved(true)
      onChanged?.()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <CosModal
      isOpen
      title="Settings"
      size="md"
      actionText="Apply"
      actionButtonProps={{ disabled: saving, loading: saving }}
      onActionClick={apply}
      onCloseClick={onClose}
    >
      <div className="flex flex-col gap-y-4">
        <label className="flex flex-col gap-y-1">
          <span className="secondary-body5 font-medium text-functional-text-secondary">
            Enterprise images folder
          </span>
          <span className="secondary-body5 text-functional-text-light">
            Directory holding the App-Framework + CubeCMP install images (rancher
            .raw, service qcow2s, cube-portal .pigz). May be a mounted USB or
            virtual media — it does not have to ship with the pxeserver, and is
            kept separate from the cluster snapshot store.
          </span>
          <div className="flex gap-x-2">
            <input
              className="primary-body4 min-w-0 flex-1 rounded-md border border-functional-border-divider px-3 py-2 outline-none focus:border-primary"
              value={imageDir}
              placeholder="/mnt/enterprise-images"
              onChange={(e) => {
                setImageDir(e.target.value)
                setSaved(false)
              }}
            />
            <button
              type="button"
              className="secondary-body4 shrink-0 rounded-md border border-functional-border-divider px-3 font-medium transition hover:border-primary hover:text-primary"
              onClick={() => (browsing ? setBrowsing(null) : openBrowse(imageDir))}
            >
              {browsing ? 'Close' : 'Browse…'}
            </button>
            <button
              type="button"
              className="secondary-body4 shrink-0 rounded-md border border-functional-border-divider px-3 font-medium transition hover:border-primary hover:text-primary"
              onClick={() => (devices ? setDevices(null) : openDevices())}
            >
              {devices ? 'Hide devices' : 'Mount media'}
            </button>
          </div>
        </label>

        {devices && (
          <div className="flex flex-col rounded-md border border-functional-border-divider">
            <div className="secondary-body5 border-b border-functional-border-divider px-3 py-2 font-medium text-functional-text-secondary">
              Block devices — mount removable media to reach the images
            </div>
            <div className="max-h-40 overflow-y-auto">
              {deviceErr && (
                <div className="secondary-body5 px-3 py-2 text-status-negative">
                  {deviceErr}
                </div>
              )}
              {devices.length === 0 ? (
                <div className="secondary-body5 px-3 py-2 text-functional-text-light">
                  (no mountable devices)
                </div>
              ) : (
                devices.map((d) => (
                  <div
                    key={d.name}
                    className="secondary-body5 flex items-center gap-x-2 px-3 py-1.5"
                  >
                    <span className="min-w-0 flex-1 truncate">
                      <span className="font-mono">{d.name}</span>
                      {d.label ? ` · ${d.label}` : ''} · {d.size} · {d.fstype}
                      {d.removable ? ' · removable' : ''}
                      {d.mountpoint ? (
                        <span className="text-status-positive">
                          {' '}
                          · mounted {d.mountpoint}
                        </span>
                      ) : null}
                    </span>
                    <button
                      type="button"
                      disabled={mountingDev === d.name}
                      className="shrink-0 rounded bg-primary px-2 py-0.5 font-medium text-grey-0 disabled:opacity-50"
                      onClick={() => doMount(d)}
                    >
                      {mountingDev === d.name
                        ? 'Mounting…'
                        : d.mountpoint
                          ? 'Use'
                          : 'Mount'}
                    </button>
                  </div>
                ))
              )}
            </div>
          </div>
        )}

        {browsing && (
          <div className="flex flex-col rounded-md border border-functional-border-divider">
            <div className="secondary-body5 flex items-center gap-x-2 border-b border-functional-border-divider px-3 py-2">
              <span className="min-w-0 flex-1 truncate font-mono text-functional-text-secondary">
                {browsing.path}
              </span>
              <button
                type="button"
                disabled={!browsing.parent}
                className="shrink-0 rounded border border-functional-border-divider px-2 py-0.5 font-medium transition hover:border-primary hover:text-primary disabled:cursor-not-allowed disabled:opacity-50"
                onClick={() => browsing.parent && openBrowse(browsing.parent)}
              >
                Up
              </button>
              <button
                type="button"
                className="shrink-0 rounded bg-primary px-2 py-0.5 font-medium text-grey-0"
                onClick={() => {
                  setImageDir(browsing.path)
                  setSaved(false)
                  setBrowsing(null)
                }}
              >
                Use this folder
              </button>
            </div>
            <div className="max-h-48 overflow-y-auto">
              {browseErr ? (
                <div className="secondary-body5 px-3 py-2 text-status-negative">
                  {browseErr}
                </div>
              ) : browsing.dirs.length === 0 ? (
                <div className="secondary-body5 px-3 py-2 text-functional-text-light">
                  (no subfolders)
                </div>
              ) : (
                browsing.dirs.map((d) => (
                  <button
                    key={d}
                    type="button"
                    className="secondary-body5 flex w-full items-center px-3 py-1.5 text-left transition hover:bg-functional-hover-grey"
                    onClick={() =>
                      openBrowse(
                        browsing.path === '/'
                          ? `/${d}`
                          : `${browsing.path}/${d}`,
                      )
                    }
                  >
                    📁 {d}
                  </button>
                ))
              )}
            </div>
          </div>
        )}

        <div className="secondary-body5 text-functional-text-light">
          Status:{' '}
          {imageDir === '' ? (
            'not set'
          ) : mounted ? (
            <span className="text-status-positive">
              mounted · App-Framework: {appfwCount} · CubeCMP: {cmpCount}
            </span>
          ) : (
            <span className="text-status-negative">not mounted / not found</span>
          )}
        </div>

        {saved && !error && (
          <CosInlineNotification type="positive" title="Saved" isClosable={false}>
            Enterprise images folder updated.
          </CosInlineNotification>
        )}
        {error && (
          <CosInlineNotification type="error" title="Error" isClosable={false}>
            {error}
          </CosInlineNotification>
        )}
      </div>
    </CosModal>
  )
}
