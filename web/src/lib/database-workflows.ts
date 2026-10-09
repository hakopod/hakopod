import { APIError } from './api'
import { editionFetch } from './client-edition'

export type ApplicationProvisioningPlan = {
  id: string
  database_id: string
  database_revision: number
  application_id: string
  application_name: string
  application_revision: number
  service: string
  variable: string
  endpoint: string
  role: string
  logical_database: string
  secret_reference: string
  privileges: string[]
  warnings: string[]
  expires_at: string
}
export type WorkflowOperation = {
  id: string
  database_id: string
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'
  phase: string
  message: string
}
export type MigrationRecoveryPlan = {
  id: string
  application_name: string
  database_name: string
  evidence_sha256: string
  warnings: string[]
  expires_at: string
  evidence: {
    database_revision: number
    application_revision: number
    profile: string
    logical_database: string
    knex_lock_rows: number
    knex_locked_rows: number
    active_migrator_sessions: number
    active_application_pods: number
    active_migration_jobs: number
    active_migration_locks: number
  }
}

export async function databaseWorkflow<T>(
  path: string,
  body: unknown,
  idempotencyKey?: string,
): Promise<T> {
  const response = await editionFetch(`/api${path}`, {
    method: 'POST',
    credentials: 'same-origin',
    cache: 'no-store',
    headers: {
      'Content-Type': 'application/json',
      ...(idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : {}),
    },
    body: JSON.stringify(body),
  })
  const payload = (await response.json().catch(() => ({}))) as {
    error?: { message?: string; code?: string }
  }
  if (!response.ok)
    throw new APIError(
      payload.error?.message || `Request failed (${response.status}).`,
      response.status,
      payload.error?.code,
    )
  return payload as T
}
