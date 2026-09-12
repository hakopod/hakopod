import type { ComponentProps, ReactNode } from 'react'
import {
  Dialog as HatchDialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@hakopod/hatch-ui/components/dialog'

export function Dialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  wide,
  sheet,
  className,
  onOpenAutoFocus,
  onCloseAutoFocus,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  children: ReactNode
  wide?: boolean
  sheet?: boolean
  className?: string
  onOpenAutoFocus?: ComponentProps<typeof DialogContent>['onOpenAutoFocus']
  onCloseAutoFocus?: ComponentProps<typeof DialogContent>['onCloseAutoFocus']
}) {
  return (
    <HatchDialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        sheet={sheet}
        className={[wide && 'dialog-wide', className].filter(Boolean).join(' ')}
        onOpenAutoFocus={onOpenAutoFocus}
        onCloseAutoFocus={onCloseAutoFocus}
      >
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {children}
      </DialogContent>
    </HatchDialog>
  )
}
