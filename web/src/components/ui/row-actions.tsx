import { Ellipsis } from 'lucide-react'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { Button } from './button'

export function RowActions({
  label,
  actions,
}: {
  label: string
  actions: { label: string; run: () => void; destructive?: boolean; disabled?: boolean }[]
}) {
  return (
    <Menu
      trigger={
        <Button variant="ghost" size="icon" aria-label={`Actions for ${label}`}>
          <Ellipsis />
        </Button>
      }
    >
      {actions.map((action) => (
        <MenuItem
          key={action.label}
          onSelect={action.run}
          destructive={action.destructive}
          disabled={action.disabled}
        >
          {action.label}
        </MenuItem>
      ))}
    </Menu>
  )
}
