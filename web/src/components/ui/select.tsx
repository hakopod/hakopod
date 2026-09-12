import { forwardRef, type SelectHTMLAttributes } from 'react'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'

// Native selection keeps large option lists and keyboard behavior inexpensive.
export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(
  function Select(props, ref) {
    return (
      <span className="native-select-wrap">
        <select ref={ref} {...props} />
        <Brackets bold />
      </span>
    )
  },
)
