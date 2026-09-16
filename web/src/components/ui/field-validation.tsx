import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type ForwardedRef,
  type FormEvent,
} from 'react'

type Control = HTMLInputElement | HTMLTextAreaElement

// Keep native constraint validation, but put its message beside the input instead
// of in a browser tooltip. The first invalid control receives keyboard focus.
export function useFieldValidation<T extends Control>(
  forwardedRef: ForwardedRef<T>,
  value: unknown,
  serverError?: string,
) {
  const control = useRef<T | null>(null)
  const [error, setError] = useState('')
  const id = useId()
  const ref = useCallback(
    (node: T | null) => {
      control.current = node
      if (typeof forwardedRef === 'function') return forwardedRef(node)
      if (forwardedRef) forwardedRef.current = node
    },
    [forwardedRef],
  )
  const update = () => {
    if (error) setError(control.current?.validationMessage || '')
  }
  useEffect(update, [value, error])
  useEffect(() => {
    if (!serverError || !control.current) return
    const input = control.current
    const group = input.form || input.closest('.form-body')
    if (input.matches(':disabled')) return
    if (group?.querySelector('[aria-invalid="true"]:not(:disabled)') === input) {
      let details = input.closest('details')
      while (details) {
        details.open = true
        details = details.parentElement?.closest('details') || null
      }
      input.focus()
    }
  }, [serverError])
  const invalid = (event: FormEvent<T>) => {
    event.preventDefault()
    setError(event.currentTarget.validationMessage || 'Check this value.')
    const first = event.currentTarget.form?.querySelector(
      'input:invalid, textarea:invalid, select:invalid',
    )
    if (!first || first === event.currentTarget) event.currentTarget.focus()
  }
  return { ref, error, id, invalid, update }
}

export function FieldError({ id, children }: { id: string; children?: string }) {
  return children ? (
    <span id={id} className="field-error" role="alert">
      {children}
    </span>
  ) : null
}
