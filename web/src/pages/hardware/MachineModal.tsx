import { CosInput, CosModal } from '@cube-frontend/ui-library'
import { useEffect, useState } from 'react'
import { Machine, MachineInput } from '../../model/machine'

export type MachineModalProps = {
  isOpen: boolean
  // Edit mode when set.
  machine?: Machine
  onCancel: () => void
  onSave: (input: MachineInput) => void
  saving?: boolean
}

export const MachineModal = (props: MachineModalProps) => {
  const { isOpen, machine, onCancel, onSave, saving } = props
  const isEdit = !!machine

  const [label, setLabel] = useState('')
  const [address, setAddress] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')

  useEffect(() => {
    if (!isOpen) return
    setLabel(machine?.label ?? '')
    setAddress(machine?.bmc.address ?? '')
    setUsername(machine?.bmc.username ?? '')
    setPassword('')
  }, [isOpen, machine])

  if (!isOpen) return null

  const valid = label.trim() !== '' && address.trim() !== ''

  return (
    <CosModal
      isOpen={isOpen}
      title={isEdit ? 'Edit machine' : 'Add machine'}
      size="sm"
      actionText="Save"
      actionButtonProps={{ disabled: !valid, loading: saving }}
      onActionClick={() => {
        const input: MachineInput = {
          label: label.trim(),
          bmc: { address: address.trim(), username: username.trim() },
        }
        // Only send a password when the user typed one; blank on edit keeps
        // the existing password.
        if (password !== '' || !isEdit) input.bmc.password = password
        onSave(input)
      }}
      onCloseClick={onCancel}
    >
      <div className="flex flex-col gap-y-4">
        <CosInput
          label="Label"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          errorMessage={label.trim() === '' ? 'Required' : undefined}
        />
        <CosInput
          label="BMC address"
          value={address}
          placeholder="10.0.0.10 or host:port"
          onChange={(e) => setAddress(e.target.value)}
          errorMessage={address.trim() === '' ? 'Required' : undefined}
        />
        <CosInput
          label="BMC username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
        <CosInput
          label="BMC password"
          type="password"
          value={password}
          placeholder={isEdit ? 'unchanged' : ''}
          onChange={(e) => setPassword(e.target.value)}
        />
      </div>
    </CosModal>
  )
}
