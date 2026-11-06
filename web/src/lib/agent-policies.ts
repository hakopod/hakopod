import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from './client'
import type { components } from './api.generated'

export type AgentPolicy = components['schemas']['AgentPolicy']
export type AgentPolicyList = components['schemas']['AgentPolicyList']
export type AgentPolicyDocument = components['schemas']['AgentPolicyDocument']
export type AgentPolicyCondition = components['schemas']['AgentPolicyCondition']
export type AgentPolicyApprover = components['schemas']['AgentPolicyApprover']
export type AgentPolicyVersion = components['schemas']['AgentPolicyVersion']
export type AgentPolicyPreview = components['schemas']['AgentPolicyPreview']
export type AgentPolicySample = components['schemas']['AgentPolicySample']
export type AgentPolicySimulation = components['schemas']['AgentPolicySimulation']
export type AgentPolicySimulationInput = components['schemas']['AgentPolicySimulationInput']
export type AgentApproval = components['schemas']['AgentApproval']
export type AgentApprovalList = components['schemas']['AgentApprovalList']

export const agentPoliciesKey = ['agent-policies'] as const
export const agentPolicyKey = (id: string) => ['agent-policies', id] as const
export const agentPolicyVersionsKey = (id: string) => ['agent-policies', id, 'versions'] as const
export const agentApprovalsKey = (state: 'pending' | 'decided') =>
  ['agent-approvals', state] as const

export const fieldLabels: Record<AgentPolicyCondition['field'], string> = {
  team: 'Owner team',
  key: 'API key',
  category: 'Action type',
  operation: 'Operation',
  project: 'Project',
  environment: 'Environment',
}
export const operatorLabels: Record<AgentPolicyCondition['operator'], string> = {
  is_any_of: 'is any of',
  is_none_of: 'is none of',
  matches: 'matches',
}
export const statusLabels: Record<AgentPolicy['status'], string> = {
  draft: 'Draft',
  enforced: 'Enforced',
  monitor: 'Monitor',
}

export function emptyDocument(): AgentPolicyDocument {
  return {
    schema_version: 1,
    description: '',
    when: [{ field: 'operation', operator: 'matches', values: [] }],
    then: {
      effect: 'require_approval',
      approvers: [{ kind: 'project_admins' }],
      approval_ttl_seconds: 900,
    },
  }
}

export function useAgentPolicies() {
  return useQuery({
    queryKey: agentPoliciesKey,
    queryFn: ({ signal }) => unwrap(client.GET('/agent-policies', { signal })),
    retry: false,
  })
}

export function useAgentPolicy(id: string) {
  return useQuery({
    queryKey: agentPolicyKey(id),
    queryFn: ({ signal }) =>
      unwrap(client.GET('/agent-policies/{id}', { params: { path: { id } }, signal })),
    retry: false,
  })
}

export function useAgentPolicyVersions(id: string) {
  return useQuery({
    queryKey: agentPolicyVersionsKey(id),
    queryFn: ({ signal }) =>
      unwrap(client.GET('/agent-policies/{id}/versions', { params: { path: { id } }, signal })),
    retry: false,
  })
}

export function createAgentPolicy(name: string, document: AgentPolicyDocument) {
  return unwrap(client.POST('/agent-policies', { body: { name, document } }))
}

export function saveAgentPolicyDraft(
  policy: AgentPolicy,
  name: string,
  document: AgentPolicyDocument,
) {
  return unwrap(
    client.PUT('/agent-policies/{id}/draft', {
      params: { path: { id: policy.id } },
      body: { expected_revision: policy.revision, name, document },
    }),
  )
}

export function publishAgentPolicy(policy: AgentPolicy, status: 'enforced' | 'monitor') {
  return unwrap(
    client.POST('/agent-policies/{id}/publish', {
      params: { path: { id: policy.id } },
      body: { expected_revision: policy.revision, status },
    }),
  )
}

export function deleteAgentPolicy(policy: AgentPolicy) {
  return unwrap(
    client.DELETE('/agent-policies/{id}', {
      params: { path: { id: policy.id }, query: { expected_revision: policy.revision } },
    }),
  )
}

export function previewAgentPolicy(
  document: AgentPolicyDocument,
  days: number,
  signal?: AbortSignal,
) {
  return unwrap(client.POST('/agent-policies/preview', { body: { document, days }, signal }))
}

export function simulateAgentPolicy(input: AgentPolicySimulationInput) {
  return unwrap(client.POST('/agent-policies/simulate', { body: input }))
}

export function useAgentApprovals(state: 'pending' | 'decided') {
  return useQuery({
    queryKey: agentApprovalsKey(state),
    queryFn: ({ signal }) =>
      unwrap(client.GET('/agent-approvals', { params: { query: { state, limit: 50 } }, signal })),
    // Pending approvals expire within minutes, so the inbox stays current.
    refetchInterval: state === 'pending' ? 15000 : false,
    retry: false,
  })
}

export function decideAgentApproval(id: string, approve: boolean, reason: string) {
  const path = approve ? '/agent-approvals/{id}/approve' : '/agent-approvals/{id}/deny'
  return unwrap(client.POST(path, { params: { path: { id } }, body: { reason } }))
}
