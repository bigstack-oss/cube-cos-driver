// Small value-select wrapper over the CosDropdown compound component.
import { CosDropdown } from '@cube-frontend/ui-library'

export type SelectOption<T> = {
  value: T
  label: string
  disabled?: boolean
}

export type SelectProps<T> = {
  label?: string
  placeholder?: string
  value: T | undefined
  options: SelectOption<T>[]
  onChange: (value: T) => void
  disabled?: boolean
  className?: string
}

export const Select = <T,>(props: SelectProps<T>) => {
  const {
    label,
    placeholder = 'Select…',
    value,
    options,
    onChange,
    disabled,
    className,
  } = props
  const selected = options.find((o) => o.value === value)
  return (
    <div className={className}>
      <CosDropdown
        type="radio"
        label={label}
        disabled={disabled}
        selectedItems={selected ? [selected] : []}
      >
        <CosDropdown.Trigger>
          {selected ? selected.label : placeholder}
        </CosDropdown.Trigger>
        <CosDropdown.Menu>
          {options.map((option) => (
            <CosDropdown.Item
              key={option.label}
              item={option}
              disabled={option.disabled}
              onClick={() => onChange(option.value)}
            >
              {option.label}
            </CosDropdown.Item>
          ))}
        </CosDropdown.Menu>
      </CosDropdown>
    </div>
  )
}
