import { forwardRef, type InputHTMLAttributes } from 'react'
import { Input as HatchInput } from '@hakopod/hatch-ui/components/input'
import { FieldError, useFieldValidation } from './field-validation'

export const Input = forwardRef<
  HTMLInputElement,
  InputHTMLAttributes<HTMLInputElement> & { error?: string }
>(function Input({ error, onInvalid, onChange, 'aria-describedby': describedBy, ...props }, ref) {
  const validation = useFieldValidation(ref, props.value, props.disabled ? undefined : error)
  const message = props.disabled ? '' : error || validation.error
  const controlProps = {
    ...props,
    ref: validation.ref,
    'aria-invalid': message ? true : props['aria-invalid'],
    'aria-describedby':
      [describedBy, message && validation.id].filter(Boolean).join(' ') || undefined,
    onInvalid: (event: React.FormEvent<HTMLInputElement>) => {
      onInvalid?.(event)
      if (!event.defaultPrevented) validation.invalid(event)
    },
    onChange: (event: React.ChangeEvent<HTMLInputElement>) => {
      onChange?.(event)
      validation.update()
    },
  }
  if (
    props.type === 'checkbox' ||
    props.type === 'radio' ||
    props.type === 'file' ||
    props.type === 'hidden'
  )
    return (
      <>
        <input {...controlProps} />
        <FieldError id={validation.id}>{message}</FieldError>
      </>
    )
  return (
    <>
      <HatchInput {...controlProps} />
      <FieldError id={validation.id}>{message}</FieldError>
    </>
  )
})
