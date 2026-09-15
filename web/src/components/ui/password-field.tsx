import { forwardRef, useId, useState, type InputHTMLAttributes } from 'react'
import { Eye, EyeOff } from 'lucide-react'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { IconButton } from '@hakopod/hatch-ui/components/icon-button'
import { FieldError, useFieldValidation } from './field-validation'

// Retain Hatch's password control while sharing dashboard validation and focus.
export const PasswordField = forwardRef<
  HTMLInputElement,
  InputHTMLAttributes<HTMLInputElement> & { label?: string; error?: string }
>(function PasswordField(
  { label = 'Password', error, onInvalid, onChange, 'aria-describedby': describedBy, ...props },
  ref,
) {
  const [visible, setVisible] = useState(false)
  const generated = useId()
  const id = props.id || generated
  const validation = useFieldValidation(ref, props.value, props.disabled ? undefined : error)
  const message = props.disabled ? '' : error || validation.error
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <div className="input-wrap password-wrap">
        <input
          {...props}
          ref={validation.ref}
          id={id}
          type={visible ? 'text' : 'password'}
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
        <IconButton
          type="button"
          disabled={props.disabled}
          label={visible ? 'Hide password' : 'Show password'}
          onClick={() => setVisible((current) => !current)}
        >
          {visible ? <EyeOff /> : <Eye />}
        </IconButton>
        <Brackets bold />
      </div>
      <FieldError id={validation.id}>{message}</FieldError>
    </div>
  )
})
