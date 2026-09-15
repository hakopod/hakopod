import { useId, useRef, useState } from 'react'
import { Field } from './ui/field'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@hakopod/hatch-ui/components/dialog'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { projectIDFromName, projectIDHelp, projectInputErrors } from '../lib/project-input'
import { Button } from './ui/button'
import { Textarea } from './ui/textarea'
import { Icon } from './icons'

export default function ProjectWizard({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (id: string, environment: string) => void
}) {
  const [step, setStep] = useState(0)
  const [displayName, setDisplayName] = useState('')
  const [id, setID] = useState('')
  const [description, setDescription] = useState('')
  const [environment, setEnvironment] = useState('development')
  const [idEdited, setIDEdited] = useState(false)
  const [showID, setShowID] = useState(true)
  const [showDescription, setShowDescription] = useState(true)
  const [errors, setErrors] = useState({
    displayName: '',
    id: '',
    description: '',
    environment: '',
  })
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const title = useRef<HTMLHeadingElement>(null)
  const descriptionID = useId()
  const values = { displayName, id, description, environment }
  const move = (next: number) => {
    setStep(next)
    setError('')
    requestAnimationFrame(() => title.current?.focus())
  }
  const validate = (name: keyof typeof errors) =>
    setErrors((current) => ({ ...current, [name]: projectInputErrors(values)[name] }))
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!busy) onOpenChange(next)
      }}
    >
      <DialogContent className="hako-project-wizard">
        <DialogHeader>
          <nav className="hako-project-steps" aria-label="Project creation steps">
            {['New project', 'Environment', 'Review'].map((label, index) => (
              <span key={label}>
                {index > 0 && <Icon name="chevron" size={14} />}
                <button
                  type="button"
                  disabled={busy || index > step}
                  aria-current={index === step ? 'step' : undefined}
                  onClick={() => move(index)}
                >
                  {index > 0 && <Icon name={index === 1 ? 'globe' : 'box'} size={16} />}
                  {label}
                </button>
              </span>
            ))}
          </nav>
          <DialogTitle ref={title} tabIndex={-1}>
            {step === 0
              ? 'Create new project'
              : step === 1
                ? 'Choose an environment'
                : 'Review your project'}
          </DialogTitle>
          <DialogDescription className="sr-only">
            Create a project and its first environment in three steps.
          </DialogDescription>
        </DialogHeader>
        <form
          noValidate
          onSubmit={async (event) => {
            event.preventDefault()
            if (busy) return
            const nextErrors = projectInputErrors(values)
            setErrors(nextErrors)
            if (step === 0) {
              if (nextErrors.id) setShowID(true)
              if (!nextErrors.displayName && !nextErrors.id && !nextErrors.description) move(1)
              return
            }
            if (step === 1) {
              if (!nextErrors.environment) move(2)
              return
            }
            if (Object.values(nextErrors).some(Boolean)) return
            setBusy(true)
            setError('')
            try {
              await unwrap(
                client.POST('/projects', {
                  body: {
                    name: id,
                    display_name: displayName.trim(),
                    description: description.trim(),
                    environment,
                  },
                }),
              )
              onCreated(id, environment)
              onOpenChange(false)
            } catch (cause) {
              setError(message(cause))
            } finally {
              setBusy(false)
            }
          }}
        >
          <div className="dialog-body hako-project-fields">
            {step === 0 ? (
              <>
                <Field
                  label="Project name"
                  placeholder="Project name"
                  required
                  maxLength={80}
                  value={displayName}
                  error={errors.displayName}
                  onBlur={() => validate('displayName')}
                  onChange={(event) => {
                    setDisplayName(event.target.value)
                    if (!idEdited) setID(projectIDFromName(event.target.value))
                  }}
                />
                {showID ? (
                  <Field
                    label="Readable ID"
                    placeholder="my-project"
                    maxLength={40}
                    required
                    value={id}
                    spellCheck={false}
                    autoComplete="off"
                    error={errors.id}
                    help="A unique, readable identifier for this project."
                    trailing={
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => {
                          setID(projectIDFromName(displayName))
                          setIDEdited(false)
                          setShowID(false)
                          setErrors((current) => ({ ...current, id: '' }))
                        }}
                      >
                        Reset & hide
                      </Button>
                    }
                    onBlur={() => validate('id')}
                    onChange={(event) => {
                      setID(event.target.value)
                      setIDEdited(true)
                    }}
                  />
                ) : (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="hako-disclosure"
                    onClick={() => setShowID(true)}
                  >
                    <Icon name="chevron" size={13} /> Edit readable ID
                    {Boolean(id) && <code>{id}</code>}
                  </Button>
                )}
                {showDescription ? (
                  <div className="field">
                    <div className="field-heading">
                      <label htmlFor={descriptionID}>Description</label>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => {
                          setDescription('')
                          setShowDescription(false)
                        }}
                      >
                        Reset & hide
                      </Button>
                    </div>
                    <Textarea
                      id={descriptionID}
                      placeholder="What is this project for?"
                      value={description}
                      maxLength={1000}
                      rows={3}
                      onChange={(event) => setDescription(event.target.value)}
                    />
                  </div>
                ) : (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="hako-disclosure"
                    onClick={() => setShowDescription(true)}
                  >
                    <Icon name="chevron" size={13} /> Set description
                  </Button>
                )}
              </>
            ) : step === 1 ? (
              <>
                <Field
                  label="Environment ID"
                  value={environment}
                  maxLength={40}
                  required
                  error={errors.environment}
                  help={projectIDHelp}
                  spellCheck={false}
                  onBlur={() => validate('environment')}
                  onChange={(event) => setEnvironment(event.target.value)}
                />
                <div className="hako-environment-picks" aria-label="Suggested environments">
                  {['development', 'staging', 'production'].map((value) => (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      key={value}
                      onClick={() => {
                        setEnvironment(value)
                        setErrors((current) => ({ ...current, environment: '' }))
                      }}
                    >
                      {value}
                    </Button>
                  ))}
                </div>
                <p className="field-help">
                  This is the first environment for your project. Applications and their access
                  remain scoped to an environment.
                </p>
              </>
            ) : (
              <dl className="hako-project-review">
                <div>
                  <dt>Project name</dt>
                  <dd>{displayName.trim()}</dd>
                </div>
                <div>
                  <dt>Readable ID</dt>
                  <dd>
                    <code>{id}</code>
                  </dd>
                </div>
                <div>
                  <dt>Description</dt>
                  <dd>{description.trim() || 'No description'}</dd>
                </div>
                <div>
                  <dt>Initial environment</dt>
                  <dd>
                    <code>{environment}</code>
                  </dd>
                </div>
              </dl>
            )}
            {error && (
              <div className="inline-error" role="alert">
                {error}
              </div>
            )}
          </div>
          <div className="dialog-footer hako-project-footer">
            <Button
              type="button"
              variant="ghost"
              disabled={busy}
              onClick={() => (step === 0 ? onOpenChange(false) : move(step - 1))}
            >
              {step === 0 ? 'Cancel' : 'Back'}
            </Button>
            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? 'Creating…' : step === 2 ? 'Create project' : 'Next'}
              {step < 2 && <Icon name="arrow" size={16} />}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
