import { CosModal, CosTag } from '@cube-frontend/ui-library'
import { Fragment } from 'react'
import {
  diskMediaType,
  formatBytes,
  formatSpeed,
  isOsEligible,
  Machine,
  pciLabel,
} from '../../model/machine'

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
              <div className="secondary-body4 grid w-fit grid-cols-[auto_auto_auto_auto_auto_auto] items-center gap-x-6 gap-y-1">
                {['Interface', 'PCI (vendor · bus)', 'MAC', 'Speed', 'Link', 'State'].map(
                  (h) => (
                    <span
                      key={h}
                      className="primary-body5 text-functional-text-light"
                    >
                      {h}
                    </span>
                  ),
                )}
                {inv.nics.map((n, i) => (
                  <Fragment key={i}>
                    <span className="font-medium">{n.name || 'nic'}</span>
                    <span>{pciLabel(n)}</span>
                    <span className="font-mono">{n.mac || '—'}</span>
                    <span>{formatSpeed(n.speedMbps)}</span>
                    <CosTag
                      variant="stroke"
                      color={
                        n.carrier === true
                          ? 'cyan'
                          : n.carrier === false
                            ? 'dark'
                            : 'default'
                      }
                    >
                      {n.carrier === true
                        ? 'up'
                        : n.carrier === false
                          ? 'down'
                          : '—'}
                    </CosTag>
                    <CosTag variant="stroke" color={n.up ? 'cyan' : 'dark'}>
                      {n.up ? 'up' : 'down'}
                    </CosTag>
                  </Fragment>
                ))}
              </div>
            </Section>
          )}

          {inv.disks && inv.disks.length > 0 && (
            <Section title={`Disks (${inv.disks.length})`}>
              <div className="secondary-body4 grid w-fit grid-cols-[auto_auto_auto_auto] items-center gap-x-6 gap-y-1">
                <span className="primary-body5 text-functional-text-light">
                  Disk
                </span>
                <span className="primary-body5 text-functional-text-light">
                  Type
                </span>
                <span className="primary-body5 text-functional-text-light">
                  Size
                </span>
                <span className="primary-body5 text-functional-text-light">
                  Media
                </span>
                {inv.disks.map((d, i) => (
                  <Fragment key={i}>
                    <span className="flex flex-col">
                      <span className="font-medium">
                        {d.name || d.model || 'disk'}
                      </span>
                      {d.model && d.name && (
                        <span className="text-functional-text-light">
                          {d.model}
                        </span>
                      )}
                    </span>
                    <span>{d.type || '—'}</span>
                    <span>{d.sizeBytes ? formatBytes(d.sizeBytes) : '—'}</span>
                    <CosTag
                      variant="stroke"
                      color={isOsEligible(d) ? 'cyan' : 'dark'}
                    >
                      {diskMediaType(d)}
                    </CosTag>
                  </Fragment>
                ))}
              </div>
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
