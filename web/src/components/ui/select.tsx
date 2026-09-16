import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type ComponentPropsWithoutRef,
  type FormEventHandler,
  type ReactNode,
} from 'react'
import * as SelectPrimitive from '@radix-ui/react-select'
import { Check, ChevronDown } from 'lucide-react'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'

export type SelectOption = { value: string; label: string; disabled?: boolean; icon?: ReactNode }
export type SelectFieldProps = Omit<
  ComponentPropsWithoutRef<typeof SelectPrimitive.Trigger>,
  | 'value'
  | 'defaultValue'
  | 'onChange'
  | 'children'
  | 'aria-label'
  | 'onInvalid'
  | 'onInvalidCapture'
> & {
  label: string
  value: string
  onValueChange: (value: string) => void
  options: readonly SelectOption[]
  compact?: boolean
  required?: boolean
  error?: string
  name?: string
  onInvalid?: FormEventHandler<HTMLSpanElement>
  onInvalidCapture?: FormEventHandler<HTMLSpanElement>
}

// Keep the Hatch API and styling while preserving forms' empty/disabled values.
export const SelectField = forwardRef<HTMLButtonElement, SelectFieldProps>(function SelectField(
  {
    label,
    value,
    onValueChange,
    options,
    compact = false,
    required,
    name,
    disabled,
    error,
    className,
    onInvalid,
    onInvalidCapture,
    'aria-describedby': describedBy,
    'aria-invalid': ariaInvalid,
    ...props
  },
  ref,
) {
  const trigger = useRef<HTMLButtonElement | null>(null)
  const triggerRef = useCallback(
    (node: HTMLButtonElement | null) => {
      trigger.current = node
      if (typeof ref === 'function') return ref(node)
      if (ref) ref.current = node
    },
    [ref],
  )
  const [invalid, setInvalid] = useState(false)
  const errorID = useId()
  const selected = options.findIndex((option) => option.value === value)
  const selectedValue = selected < 0 || (required && value === '') ? '' : `option-${selected}`
  const emptyLabel = options.find((option) => option.value === '')?.label || 'Choose an option'
  const showInvalid = invalid && required && !disabled && selectedValue === ''
  useEffect(() => {
    if (selectedValue !== '' || !required || disabled) setInvalid(false)
  }, [selectedValue, required, disabled])
  useEffect(() => {
    const input = trigger.current
    if (!error || !input || input.matches(':disabled')) return
    const group = input.closest('form, .form-body')
    if (group?.querySelector('[aria-invalid="true"]:not(:disabled)') === input) {
      const details = input.closest('details')
      if (details) details.open = true
      input.focus()
    }
  }, [error])
  return (
    <span
      className="select-field-wrap"
      onInvalidCapture={onInvalidCapture}
      onInvalid={(event) => {
        onInvalid?.(event)
        if (event.defaultPrevented) return
        event.preventDefault()
        setInvalid(true)
        const control = event.target as HTMLSelectElement
        const firstInvalid = control.form?.querySelector(
          'input:invalid, select:invalid, textarea:invalid',
        )
        if (!firstInvalid || firstInvalid === control) trigger.current?.focus()
      }}
    >
      {name && <input type="hidden" name={name} value={value} disabled={disabled} />}
      <SelectPrimitive.Root
        value={selectedValue}
        disabled={disabled}
        required={required}
        onValueChange={(next) => {
          const option = options[Number(next.slice('option-'.length))]
          if (option && !option.disabled) {
            if (option.value || !required) setInvalid(false)
            onValueChange(option.value)
          }
        }}
      >
        <SelectPrimitive.Trigger
          {...props}
          ref={triggerRef}
          aria-label={label}
          aria-invalid={ariaInvalid ?? (Boolean(error) || showInvalid || undefined)}
          aria-describedby={
            [describedBy, (error || showInvalid) && errorID].filter(Boolean).join(' ') || undefined
          }
          className={`select-trigger interactive${compact ? ' compact' : ''}${className ? ` ${className}` : ''}`}
        >
          <span className="select-field-value">
            {selected >= 0 && options[selected].icon && (
              <span className="mr-2 inline-flex shrink-0 items-center" aria-hidden="true">
                {options[selected].icon}
              </span>
            )}
            <SelectPrimitive.Value placeholder={emptyLabel}>
              {selected >= 0 ? options[selected].label : emptyLabel}
            </SelectPrimitive.Value>
          </span>
          <SelectPrimitive.Icon>
            <ChevronDown size={14} aria-hidden="true" />
          </SelectPrimitive.Icon>
          <Brackets />
        </SelectPrimitive.Trigger>
        <SelectPrimitive.Portal>
          <SelectPrimitive.Content
            position="popper"
            sideOffset={6}
            collisionPadding={12}
            className="select-content hako-select-content"
          >
            <SelectPrimitive.Viewport>
              {options.map((option, index) => (
                <SelectPrimitive.Item
                  className="select-item"
                  key={option.value}
                  value={`option-${index}`}
                  disabled={option.disabled}
                >
                  {option.icon && (
                    <span className="mr-2 inline-flex shrink-0 items-center" aria-hidden="true">
                      {option.icon}
                    </span>
                  )}
                  <span className="min-w-0 flex-1">
                    <SelectPrimitive.ItemText>{option.label}</SelectPrimitive.ItemText>
                  </span>
                  <SelectPrimitive.ItemIndicator>
                    <Check size={14} aria-hidden="true" />
                  </SelectPrimitive.ItemIndicator>
                </SelectPrimitive.Item>
              ))}
            </SelectPrimitive.Viewport>
          </SelectPrimitive.Content>
        </SelectPrimitive.Portal>
      </SelectPrimitive.Root>
      {(error || showInvalid) && (
        <span id={errorID} className="field-help error" role="alert">
          {error || 'Choose an option.'}
        </span>
      )}
    </span>
  )
})
