// Shows how each node pulls its snapshot: `snapshot pull url <hostname>`
// works out of the box on the pxeserver (lighttpd serves the export dir);
// full URLs cover custom deployments.
import { CosModal } from '@cube-frontend/ui-library'
import { pullUrl } from '../../api/client'

export type SnapshotUrlModalProps = {
  isOpen: boolean
  hostnames: string[]
  onClose: () => void
}

export const SnapshotUrlModal = (props: SnapshotUrlModalProps) => {
  const { isOpen, hostnames, onClose } = props
  if (!isOpen) return null
  const host = window.location.hostname || '192.168.1.150'
  return (
    <CosModal
      isOpen={isOpen}
      title="Fetch snapshots from a node"
      size="md"
      isActionButtonVisible={false}
      onCloseClick={onClose}
    >
      <div className="flex flex-col gap-y-4">
        <p className="primary-body3">
          After PXE re-imaging, run this on each node's CLI (
          <code>snapshot</code> menu), then <code>snapshot apply</code>:
        </p>
        <pre className="secondary-body4 overflow-x-auto rounded-md bg-scene-background p-3">
          {hostnames
            .map((h) => `snapshot pull url ${pullUrl(host, h)}`)
            .join('\n')}
        </pre>
        <p className="secondary-body5 text-functional-text-light">
          On the pxeserver, the bare name is enough: `snapshot pull url
          &lt;hostname&gt;`.
        </p>
      </div>
    </CosModal>
  )
}
