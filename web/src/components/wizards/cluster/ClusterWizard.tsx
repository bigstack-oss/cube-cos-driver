// Cluster settings wizard: License → Name → DNS → Timezone → Role settings →
// High availability. Edit mode (initialConfig set) skips the license step.
import { CosCheckbox, CosInput, CosToggle } from '@cube-frontend/ui-library'
import { useEffect, useState } from 'react'
import {
  isValidCIDR,
  isValidHostname,
  isValidIPv4,
} from '../../../model/validate'
import {
  ClusterConfig,
  ClusterInfo,
  Timezone,
} from '../../../model/types'
import { genRandomChars } from '../../../utils/random'
import { Select } from '../../form/Select'
import { WizardModal, WizardStep } from '../../WizardModal'
import { getTimezoneOptions, guessTimezone } from './timezones'

export type ClusterWizardProps = {
  isOpen: boolean
  // Edit mode when set: prefills and skips the license step.
  initialInfo?: ClusterInfo
  initialConfig?: ClusterConfig
  newClusterId: string
  onCancel: () => void
  onFinish: (info: ClusterInfo, config: ClusterConfig) => void
}

const defaultConfig = (): ClusterConfig => ({
  DNS: ['8.8.8.8'],
  timezone: guessTimezone(),
  roleSettings: {
    extIP: '',
    region: 'RegionOne',
    secretSeed: genRandomChars(6),
    mgmtCIDR: '10.254.0.0/16',
  },
  HA: false,
  HASettings: {},
})

export const ClusterWizard = (props: ClusterWizardProps) => {
  const { isOpen, initialInfo, initialConfig, newClusterId, onCancel, onFinish } =
    props
  const isEdit = !!initialConfig

  const [licenseAccepted, setLicenseAccepted] = useState(false)
  const [name, setName] = useState('')
  const [config, setConfig] = useState<ClusterConfig>(defaultConfig)

  useEffect(() => {
    if (!isOpen) return
    setLicenseAccepted(isEdit)
    setName(initialInfo?.name ?? `Cluster_${genRandomChars(6)}`)
    setConfig(initialConfig ?? defaultConfig())
  }, [isOpen, isEdit, initialInfo, initialConfig])

  const patch = (partial: Partial<ClusterConfig>) =>
    setConfig((prev) => ({ ...prev, ...partial }))

  const dnsValid =
    config.DNS.length >= 1 &&
    config.DNS.every((d) => isValidIPv4(d)) &&
    config.DNS.length <= 2

  const roleSettingsValid =
    (config.roleSettings.extIP === '' ||
      isValidIPv4(config.roleSettings.extIP)) &&
    config.roleSettings.region !== '' &&
    config.roleSettings.secretSeed !== '' &&
    isValidCIDR(config.roleSettings.mgmtCIDR)

  const haValid =
    !config.HA ||
    (isValidIPv4(config.HASettings.virtualIP ?? '') &&
      isValidHostname(config.HASettings.virtualHostname ?? ''))

  const steps: WizardStep[] = []

  if (!isEdit) {
    steps.push({
      label: 'License',
      canNext: licenseAccepted,
      content: (
        <div className="flex flex-col gap-y-4">
          <p className="primary-body2">
            The generated snapshots configure CubeCOS nodes. By continuing you
            confirm the target machines hold valid CubeCOS licenses.
          </p>
          <CosCheckbox
            label="I understand and accept"
            checked={licenseAccepted}
            onChange={(e) => setLicenseAccepted(e.target.checked)}
          />
        </div>
      ),
    })
  }

  steps.push({
    label: 'Name',
    canNext: name.trim() !== '',
    content: (
      <CosInput
        label="Cluster name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        errorMessage={name.trim() === '' ? 'The field should not be blank' : undefined}
      />
    ),
  })

  steps.push({
    label: 'DNS',
    canNext: dnsValid,
    content: (
      <div className="flex flex-col gap-y-4">
        {config.DNS.map((dns, i) => (
          <CosInput
            key={i}
            label={i === 0 ? 'Primary DNS' : 'Secondary DNS'}
            value={dns}
            onChange={(e) => {
              const next = [...config.DNS]
              next[i] = e.target.value
              patch({ DNS: next })
            }}
            errorMessage={!isValidIPv4(dns) ? 'Invalid IPv4 address' : undefined}
          />
        ))}
        <CosToggle
          label="Use a secondary DNS"
          isOn={config.DNS.length > 1}
          onChange={(on) =>
            patch({ DNS: on ? [config.DNS[0], ''] : [config.DNS[0]] })
          }
        />
      </div>
    ),
  })

  steps.push({
    label: 'Timezone',
    canNext: config.timezone.name !== '',
    content: (
      <Select<Timezone>
        label="Timezone"
        value={getTimezoneOptions().find(
          (o) => o.value.name === config.timezone.name,
        )?.value}
        options={getTimezoneOptions()}
        onChange={(timezone) => patch({ timezone })}
      />
    ),
  })

  steps.push({
    label: 'Role settings',
    canNext: roleSettingsValid,
    content: (
      <div className="flex flex-col gap-y-4">
        <CosInput
          label="External IP (optional)"
          value={config.roleSettings.extIP}
          onChange={(e) =>
            patch({
              roleSettings: { ...config.roleSettings, extIP: e.target.value },
            })
          }
          errorMessage={
            config.roleSettings.extIP !== '' &&
            !isValidIPv4(config.roleSettings.extIP)
              ? 'Invalid IPv4 address'
              : undefined
          }
        />
        <CosInput
          label="Region"
          value={config.roleSettings.region}
          onChange={(e) =>
            patch({
              roleSettings: { ...config.roleSettings, region: e.target.value },
            })
          }
          errorMessage={
            config.roleSettings.region === ''
              ? 'The field should not be blank'
              : undefined
          }
        />
        <CosInput
          label="Secret seed"
          value={config.roleSettings.secretSeed}
          onChange={(e) =>
            patch({
              roleSettings: {
                ...config.roleSettings,
                secretSeed: e.target.value,
              },
            })
          }
          errorMessage={
            config.roleSettings.secretSeed === ''
              ? 'The field should not be blank'
              : undefined
          }
        />
        <CosInput
          label="Management CIDR"
          value={config.roleSettings.mgmtCIDR}
          onChange={(e) =>
            patch({
              roleSettings: {
                ...config.roleSettings,
                mgmtCIDR: e.target.value,
              },
            })
          }
          errorMessage={
            !isValidCIDR(config.roleSettings.mgmtCIDR)
              ? 'Invalid CIDR (e.g. 10.254.0.0/16)'
              : undefined
          }
        />
      </div>
    ),
  })

  steps.push({
    label: 'High availability',
    canNext: haValid,
    content: (
      <div className="flex flex-col gap-y-4">
        <CosToggle
          label="High availability"
          isOn={config.HA}
          onChange={(on) =>
            patch({
              HA: on,
              HASettings: on ? { virtualIP: '', virtualHostname: '' } : {},
            })
          }
        />
        {config.HA && (
          <>
            <CosInput
              label="Virtual IP"
              value={config.HASettings.virtualIP ?? ''}
              onChange={(e) =>
                patch({
                  HASettings: {
                    ...config.HASettings,
                    virtualIP: e.target.value,
                  },
                })
              }
              errorMessage={
                !isValidIPv4(config.HASettings.virtualIP ?? '')
                  ? 'Invalid IPv4 address'
                  : undefined
              }
            />
            <CosInput
              label="Virtual hostname"
              value={config.HASettings.virtualHostname ?? ''}
              onChange={(e) =>
                patch({
                  HASettings: {
                    ...config.HASettings,
                    virtualHostname: e.target.value,
                  },
                })
              }
              errorMessage={
                !isValidHostname(config.HASettings.virtualHostname ?? '')
                  ? 'Invalid hostname'
                  : undefined
              }
            />
          </>
        )}
      </div>
    ),
  })

  return (
    <WizardModal
      isOpen={isOpen}
      title="Cluster Snapshot Configuration"
      steps={steps}
      finishText="Save"
      onCancel={onCancel}
      onFinish={() =>
        onFinish(
          { id: initialInfo?.id ?? newClusterId, name: name.trim() },
          config,
        )
      }
    />
  )
}
