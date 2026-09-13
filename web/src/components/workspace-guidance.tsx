import { Link } from '@tanstack/react-router'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
  SheetBody,
} from '@hakopod/hatch-ui/components/sheet'
import { useScope } from '../lib/scope'
import { Icon } from './icons'
import { Button } from './ui/button'

export default function WorkspaceGuidance({
  open,
  onOpenChange,
  onCreateProject,
  onCommands,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreateProject: () => void
  onCommands: () => void
}) {
  const scope = useScope()
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="hako-assistant-sheet">
        <SheetHeader>
          <SheetTitle>How can I help?</SheetTitle>
          <SheetDescription>
            Guided actions for your workspace. AI chat is not connected.
          </SheetDescription>
        </SheetHeader>
        <SheetBody>
          <div className="hako-guidance-scope">
            <span>Current workspace</span>
            <code>
              {scope.project
                ? `${scope.project} / ${scope.environment || 'Select environment'}`
                : 'No project selected'}
            </code>
          </div>
          <div className="hako-guidance-actions">
            {scope.identity.admin && (
              <button
                type="button"
                className="interactive"
                onClick={() => {
                  onOpenChange(false)
                  onCreateProject()
                }}
              >
                <Icon name="plus" />
                <span>
                  <strong>Create a project</strong>
                  <small>Name a project and choose its first environment.</small>
                </span>
                <Icon name="chevron" size={15} />
              </button>
            )}
            <Link to="/templates" onClick={() => onOpenChange(false)} className="interactive">
              <Icon name="box" />
              <span>
                <strong>Find an application template</strong>
                <small>Browse the catalog and review its configuration.</small>
              </span>
              <Icon name="chevron" size={15} />
            </Link>
            <Button variant="ghost" asChild>
              {scope.project ? (
                <Link
                  to="/projects/$project"
                  params={{ project: scope.project }}
                  search={{ environment: scope.environment || undefined }}
                  onClick={() => onOpenChange(false)}
                >
                  <Icon name="activity" />
                  <span>
                    <strong>Inspect applications</strong>
                    <small>Open an application to review health, deployments, and logs.</small>
                  </span>
                  <Icon name="chevron" size={15} />
                </Link>
              ) : (
                <Link to="/" onClick={() => onOpenChange(false)}>
                  <Icon name="grid" />
                  <span>
                    <strong>Choose a project</strong>
                    <small>Open a project to inspect its applications and environments.</small>
                  </span>
                  <Icon name="chevron" size={15} />
                </Link>
              )}
            </Button>
            <Link
              to="/settings"
              search={{ tab: scope.identity.admin ? 'teams' : 'account' }}
              onClick={() => onOpenChange(false)}
              className="interactive"
            >
              <Icon name="shield" />
              <span>
                <strong>Manage access</strong>
                <small>
                  {scope.identity.admin
                    ? 'Review teams, project membership, and permissions.'
                    : 'Review your sessions and sign-in methods.'}
                </small>
              </span>
              <Icon name="chevron" size={15} />
            </Link>
          </div>
          <Button
            variant="outline"
            onClick={() => {
              onOpenChange(false)
              onCommands()
            }}
          >
            <Icon name="search" size={16} /> Search navigation
          </Button>
          <p className="field-help">
            These shortcuts open the real controls. Changes are reviewed and submitted there.
          </p>
        </SheetBody>
      </SheetContent>
    </Sheet>
  )
}
