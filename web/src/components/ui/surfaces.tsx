import type { ReactNode } from 'react'
import { Badge as HatchBadge } from '@hakopod/hatch-ui/components/badge'
import {
  Tooltip as HatchTooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@hakopod/hatch-ui/components/tooltip'
export { Card } from '@hakopod/hatch-ui/components/card'

export function Badge({
  children,
  tone = 'neutral',
}: {
  children: ReactNode
  tone?: 'neutral' | 'accent' | 'success' | 'warning' | 'danger'
}) {
  return (
    <HatchBadge tone={tone === 'accent' ? 'state' : tone === 'danger' ? 'error' : tone}>
      {children}
    </HatchBadge>
  )
}

export function Tooltip({
  children,
  content,
  side = 'top',
  open,
  onOpenChange,
}: {
  children: ReactNode
  content: ReactNode
  side?: 'top' | 'right' | 'bottom' | 'left'
  open?: boolean
  onOpenChange?: (open: boolean) => void
}) {
  return (
    <TooltipProvider delayDuration={250}>
      <HatchTooltip open={open} onOpenChange={onOpenChange}>
        <TooltipTrigger asChild>{children}</TooltipTrigger>
        <TooltipContent className="hako-tooltip" side={side} collisionPadding={8}>
          {content}
        </TooltipContent>
      </HatchTooltip>
    </TooltipProvider>
  )
}
