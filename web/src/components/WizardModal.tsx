// Multi-step modal built on CosModal + CosStepProcess. The footerMessage
// slot hosts the Back button; the modal action advances or finishes.
import { CosButton, CosModal, CosStepProcess } from '@cube-frontend/ui-library'
import { ReactNode, useEffect, useState } from 'react'

export type WizardStep = {
  label: string
  content: ReactNode
  // Blocks Next/Finish while false.
  canNext: boolean
}

export type WizardModalProps = {
  isOpen: boolean
  title: string
  steps: WizardStep[]
  finishText?: string
  onCancel: () => void
  onFinish: () => void
}

export const WizardModal = (props: WizardModalProps) => {
  const { isOpen, title, steps, finishText = 'Save', onCancel, onFinish } = props
  const [stepIndex, setStepIndex] = useState(0)

  useEffect(() => {
    if (isOpen) setStepIndex(0)
  }, [isOpen])

  if (!isOpen) return null
  const isLast = stepIndex === steps.length - 1
  const step = steps[stepIndex]

  return (
    <CosModal
      isOpen={isOpen}
      title={title}
      size="md"
      actionText={isLast ? finishText : 'Next'}
      actionButtonProps={{ disabled: !step.canNext }}
      onActionClick={() => {
        if (!isLast) {
          setStepIndex(stepIndex + 1)
          return
        }
        onFinish()
      }}
      onCloseClick={onCancel}
      footerMessage={
        stepIndex > 0 ? (
          <CosButton type="secondary" onClick={() => setStepIndex(stepIndex - 1)}>
            Back
          </CosButton>
        ) : undefined
      }
    >
      <div className="flex min-h-80 flex-col gap-y-6">
        <CosStepProcess>
          {steps.map((s, i) => (
            <CosStepProcess.Item
              key={s.label}
              stepNumber={i + 1}
              label={s.label}
              isActive={i === stepIndex}
            />
          ))}
        </CosStepProcess>
        <div className="flex-1">{step.content}</div>
      </div>
    </CosModal>
  )
}
