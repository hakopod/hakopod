import { useId, type InputHTMLAttributes, type ReactNode } from 'react'
import { Input } from './input'

export function Field({
  label,
  help,
  trailing,
  'aria-describedby': describedBy,
  ...props
}: InputHTMLAttributes<HTMLInputElement> & {
  label: string
  error?: string
  help?: ReactNode
  trailing?: ReactNode
}) {
  const generated = useId()
  const id = props.id || generated
  return (
    <div className="field">
      <div className="field-heading">
        <label htmlFor={id}>{label}</label>
        {trailing}
      </div>
      <Input
        {...props}
        id={id}
        aria-describedby={
          [describedBy, help && `${id}-help`].filter(Boolean).join(' ') || undefined
        }
      />
      {help && (
        <div id={`${id}-help`} className="field-help">
          {help}
        </div>
      )}
    </div>
  )
}
