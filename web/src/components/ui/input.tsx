import { forwardRef, type InputHTMLAttributes } from 'react'
import { Input as HatchInput } from '@hakopod/hatch-ui/components/input'

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(
  function Input(props, ref) {
    if (
      props.type === 'checkbox' ||
      props.type === 'radio' ||
      props.type === 'file' ||
      props.type === 'hidden'
    )
      return <input ref={ref} {...props} />
    return <HatchInput ref={ref} {...props} />
  },
)
