import type { ReactNode } from 'react'
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@hakopod/hatch-ui/components/dialog'

/**
 * A modal inspection sheet: identity, essential state and a named close in the header,
 * a body that scrolls inside, and short actions in the footer. It blocks the page, so it
 * keeps Radix dialog semantics (focus containment, Escape, focus return to the opener).
 * Open one at a time; a sheet never opens another sheet. Edits with more than four
 * inputs link to their task page instead.
 */
export function InspectorSheet({
  open,
  onOpenChange,
  title,
  kind,
  description,
  state,
  footer,
  wide = false,
  children,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  /** Names the close control, for example “pod” → “Close pod inspector”. */
  kind: string
  description: string
  state?: ReactNode
  footer?: ReactNode
  /** 720px for detailed logs; 480px otherwise. */
  wide?: boolean
  children: ReactNode
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        sheet
        className={wide ? 'inspect-sheet inspect-sheet-wide' : 'inspect-sheet'}
        closeLabel={`Close ${kind} inspector`}
      >
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {state && <div className="inspect-sheet-state">{state}</div>}
          <DialogDescription className="dialog-description">{description}</DialogDescription>
        </DialogHeader>
        <DialogBody>{children}</DialogBody>
        {footer && <DialogFooter>{footer}</DialogFooter>}
      </DialogContent>
    </Dialog>
  )
}
