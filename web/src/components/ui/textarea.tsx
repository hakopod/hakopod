import { forwardRef, type ComponentPropsWithoutRef } from 'react'
import { Textarea as HatchTextarea } from '@hakopod/hatch-ui/components/textarea'
import { FieldError, useFieldValidation } from './field-validation'

export const Textarea = forwardRef<
  HTMLTextAreaElement,
  ComponentPropsWithoutRef<typeof HatchTextarea> & { error?: string }
>(function Textarea(
  { error, onInvalid, onChange, 'aria-describedby': describedBy, ...props },
  ref,
) {
  const validation = useFieldValidation(ref, props.value, props.disabled ? undefined : error)
  const message = props.disabled ? '' : error || validation.error
  return (
    <>
      <HatchTextarea
        {...props}
        ref={validation.ref}
        aria-invalid={message ? true : props['aria-invalid']}
        aria-describedby={
          [describedBy, message && validation.id].filter(Boolean).join(' ') || undefined
        }
        onInvalid={(event) => {
          onInvalid?.(event)
          if (!event.defaultPrevented) validation.invalid(event)
        }}
        onChange={(event) => {
          onChange?.(event)
          validation.update()
        }}
      />
      <FieldError id={validation.id}>{message}</FieldError>
    </>
  )
})
