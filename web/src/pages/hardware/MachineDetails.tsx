import { CosModal } from '@cube-frontend/ui-library'
import { formatBytes, Machine } from '../../model/machine'

export type MachineDetailsProps = {
  machine: Machine | null
  onClose: () => void
}

const Section = (props: { title: string; children: React.ReactNode }) => (
  <div className="flex flex-col gap-y-1">
    <span className="primary-body3 font-semibold">{props.title}</span>
    {props.children}
  </div>
)

export const MachineDetails = (props: MachineDetailsProps) => {
  const { machine, onClose } = props
  if (!machine) return null
  const inv = machine.inventory

  return (
    <CosModal
      isOpen
      title={`${machine.label} — hardware`}
      size="md"
      isActionButtonVisible={false}
      cancelButtonProps={{}}
      onCloseClick={onClose}
    >
      {!inv ? (
        <p className="primary-body3">
          No hardware data yet. Use <strong>Fetch</strong> to read it from the
          BMC.
        </p>
      ) : (
        <div className="flex flex-col gap-y-5">
          <div className="grid grid-cols-2 gap-3 md:grid-cols-3">
            <Field label="Source" value={inv.source} />
            <Field label="Serial" value={inv.serial} />
            <Field label="Manufacturer" value={inv.manufacturer} />
            <Field label="Model" value={inv.model} />
            <Field
              label="CPU"
              value={
                inv.cpuModel
                  ? `${inv.cpuModel} ×${inv.cpuCount ?? 1}`
                  : inv.cpuCount
                    ? `×${inv.cpuCount}`
                    : undefined
              }
            />
            <Field label="Cores" value={inv.cpuCores?.toString()} />
            <Field label="Memory" value={formatBytes(inv.memoryBytes)} />
            <Field label="Fetched" value={inv.fetchedAt} />
          </div>

          {inv.nics && inv.nics.length > 0 && (
            <Section title={`NICs (${inv.nics.length})`}>
              <ul className="secondary-body4 flex flex-col gap-y-0.5">
                {inv.nics.map((n, i) => (
                  <li key={i}>
                    {(n.name || 'nic') + ' '}
                    {n.mac && <span>· {n.mac}</span>}
                    {n.speedMbps ? <span> · {n.speedMbps} Mbps</span> : null}
                    {n.up ? <span> · up</span> : null}
                  </li>
                ))}
              </ul>
            </Section>
          )}

          {inv.disks && inv.disks.length > 0 && (
            <Section title={`Disks (${inv.disks.length})`}>
              <ul className="secondary-body4 flex flex-col gap-y-0.5">
                {inv.disks.map((d, i) => (
                  <li key={i}>
                    {(d.name || d.model || 'disk') + ' '}
                    {d.type && <span>· {d.type}</span>}
                    {d.sizeBytes ? (
                      <span> · {formatBytes(d.sizeBytes)}</span>
                    ) : null}
                  </li>
                ))}
              </ul>
            </Section>
          )}

          {inv.cards && inv.cards.length > 0 && (
            <Section title={`Cards (${inv.cards.length})`}>
              <ul className="secondary-body4 flex flex-col gap-y-0.5">
                {inv.cards.map((c, i) => (
                  <li key={i}>
                    {(c.name || 'card') + ' '}
                    {c.type && <span>· {c.type}</span>}
                    {c.slot && <span> · slot {c.slot}</span>}
                  </li>
                ))}
              </ul>
            </Section>
          )}
        </div>
      )}
    </CosModal>
  )
}

const Field = (props: { label: string; value?: string }) => (
  <div className="flex flex-col">
    <span className="secondary-body5 text-functional-text-light">
      {props.label}
    </span>
    <span className="primary-body3">{props.value || '—'}</span>
  </div>
)
