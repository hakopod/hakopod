import { Link } from '@tanstack/react-router'
import { Button } from './ui/button'

export function GitDeploymentPaths({ active }: { active: 'build' | 'config' }) {
  return (
    <nav aria-label="Deployment method" className="grid gap-2 py-4">
      <div className="flex flex-wrap items-center gap-2">
        <Button asChild variant="ghost" className="git-deployment-method">
          <Link to="/builds/new" aria-current={active === 'build' ? 'page' : undefined}>
            Build from Git
          </Link>
        </Button>
        <Button asChild variant="ghost" className="git-deployment-method">
          <Link to="/applications/import" aria-current={active === 'config' ? 'page' : undefined}>
            Import Git configuration
          </Link>
        </Button>
        <Button asChild variant="ghost" className="git-deployment-method">
          <Link to="/applications/new" search={{ mode: 'form' as const }}>
            Deploy a container image
          </Link>
        </Button>
      </div>
      <p className="field-help">
        {active === 'build'
          ? 'Detect your framework, use Cloud Native Buildpacks, or build a Dockerfile. No Hakopod configuration file is required.'
          : 'Import a repository that already contains a Hakopod TOML file. For source code without that file, choose Build from Git.'}
      </p>
    </nav>
  )
}
