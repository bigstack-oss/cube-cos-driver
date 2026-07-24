import { CosButton, CosGeneralPanel, CosTag } from '@cube-frontend/ui-library'
import { ClusterConfig, ClusterInfo } from '../../model/types'

const Item = (props: { label: string; value: string }) => (
  <div className="flex flex-col">
    <span className="secondary-body5 text-functional-text-light">
      {props.label}
    </span>
    <span className="primary-body3">{props.value || '—'}</span>
  </div>
)

export type ClusterDetailCardProps = {
  info: ClusterInfo
  config: ClusterConfig
  onEdit: () => void
}

export const ClusterDetailCard = (props: ClusterDetailCardProps) => {
  const { info, config, onEdit } = props
  return (
    <CosGeneralPanel
      topic={info.name}
      rightSlot={
        <CosButton type="secondary" size="sm" onClick={onEdit}>
          Edit
        </CosButton>
      }
    >
      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        <Item label="DNS" value={config.DNS.join(', ')} />
        <Item label="Timezone" value={config.timezone.name} />
        <Item label="Region" value={config.roleSettings.region} />
        <Item label="External IP" value={config.roleSettings.extIP} />
        <Item label="Management CIDR" value={config.roleSettings.mgmtCIDR} />
        <Item label="Secret seed" value={config.roleSettings.secretSeed} />
        <div className="flex flex-col">
          <span className="secondary-body5 text-functional-text-light">
            High availability
          </span>
          <CosTag
            variant="filled"
            color={config.HA ? 'primary-blue' : 'default'}
            className="w-fit"
          >
            {config.HA ? 'HA' : 'Standalone'}
          </CosTag>
        </div>
        {config.HA && (
          <Item
            label="Virtual IP / hostname"
            value={`${config.HASettings.virtualIP} / ${config.HASettings.virtualHostname}`}
          />
        )}
      </div>
    </CosGeneralPanel>
  )
}
