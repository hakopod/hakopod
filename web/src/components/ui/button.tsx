import { forwardRef, type ComponentProps } from 'react'
import { Button as HatchButton } from '@hakopod/hatch-ui/components/button'

type Props = Omit<ComponentProps<typeof HatchButton>, 'variant' | 'size'> & {
  variant?: ComponentProps<typeof HatchButton>['variant'] | 'danger'
  size?: ComponentProps<typeof HatchButton>['size'] | 'default'
}

// Secondary is the console default; each surface chooses one primary action.
export const Button = forwardRef<HTMLButtonElement, Props>(function Button(
  { variant = 'secondary', size = 'md', ...props },
  ref,
) {
  return (
    <HatchButton
      ref={ref}
      variant={variant === 'danger' ? 'destructive' : variant}
      size={size === 'default' ? 'md' : size}
      {...props}
    />
  )
})
