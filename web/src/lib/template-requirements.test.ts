import assert from 'node:assert/strict'
import test from 'node:test'
import {
  activeTemplateConfigFields,
  activeTemplateConfigValues,
  unsupportedHostedTemplateRequirements,
  workloadRequirementLabel,
} from './template-requirements'

test('paid hosted compute with ordinary storage keeps shared-storage templates blocked', () => {
  const features = { hostedFree: false, hostedStorageGiB: 20 }
  const requirements = ['larger_service', 'multiple_services', 'persistent_storage']
  const blocked = unsupportedHostedTemplateRequirements(
    { id: 'mathesar', workload_requirements: [...requirements, 'shared_storage'] },
    features,
  )
  assert.deepEqual(blocked, ['shared_storage'])
  assert.equal(workloadRequirementLabel[blocked[0]], 'Shared storage (ReadWriteMany)')
  assert.deepEqual(
    unsupportedHostedTemplateRequirements(
      { id: 'xem', workload_requirements: requirements },
      features,
    ),
    [],
  )
})

test('database mode changes retain external drafts but submit only active configuration', () => {
  const fields = [
    {
      name: 'database-mode',
      label: 'Database',
      description: '',
      default: 'bundled',
      required: true,
    },
    {
      name: 'media-storage-class',
      label: 'Media storage',
      description: '',
      default: '',
      required: true,
    },
    {
      name: 'database-host',
      label: 'Host',
      description: '',
      default: '',
      required: true,
      when: { field: 'database-mode', value: 'external' },
    },
  ]
  const drafts = {
    'database-mode': 'bundled',
    'media-storage-class': 'shared-media',
    'database-host': 'postgres.example.invalid',
  }
  assert.deepEqual(activeTemplateConfigValues(fields, drafts), {
    'database-mode': 'bundled',
    'media-storage-class': 'shared-media',
  })
  assert.equal(activeTemplateConfigFields(fields, drafts).length, 2)
  drafts['database-mode'] = 'external'
  assert.equal(
    activeTemplateConfigValues(fields, drafts)['database-host'],
    'postgres.example.invalid',
  )
  assert.equal(activeTemplateConfigFields(fields, drafts).length, 3)
  drafts['database-mode'] = 'bundled'
  assert.equal('database-host' in activeTemplateConfigValues(fields, drafts), false)
  assert.equal(drafts['database-host'], 'postgres.example.invalid')
})

test('database and Redis connection fields activate independently', () => {
  const field = (name: string, when?: { field: string; value: string }) => ({
    name,
    label: name,
    description: '',
    default: '',
    required: true,
    when,
  })
  const fields = [
    field('database-mode'),
    field('redis-mode'),
    field('database-host', { field: 'database-mode', value: 'external' }),
    field('redis-host', { field: 'redis-mode', value: 'external' }),
  ]
  for (const database of ['bundled', 'external']) {
    for (const redis of ['bundled', 'external']) {
      const values = {
        'database-mode': database,
        'redis-mode': redis,
        'database-host': 'postgres.example.invalid',
        'redis-host': 'redis.example.invalid',
      }
      const submitted = activeTemplateConfigValues(fields, values)
      assert.equal('database-host' in submitted, database === 'external')
      assert.equal('redis-host' in submitted, redis === 'external')
      assert.equal(values['database-host'], 'postgres.example.invalid')
      assert.equal(values['redis-host'], 'redis.example.invalid')
    }
  }
})
