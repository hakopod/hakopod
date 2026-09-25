import type { Service } from './types'

export type RunnerResources = NonNullable<Service['resources']>

export function runnerReservation(resources: RunnerResources, replicas: number) {
  const cpu = resources.cpu_request?.trim() || ''
  const memory = resources.memory_request?.trim() || ''
  const match = memory.match(/^([0-9]+)(Ki|Mi|Gi|Ti|k|M|G|T)?$/)
  if (!/^(?:[0-9]+(?:\.[0-9]{1,3})?|[0-9]+m)$/.test(cpu) || !match) return undefined
  const units: Record<string, number> = {
    Ki: 1024,
    Mi: 1024 ** 2,
    Gi: 1024 ** 3,
    Ti: 1024 ** 4,
    k: 1000,
    M: 1000 ** 2,
    G: 1000 ** 3,
    T: 1000 ** 4,
  }
  const cores = cpu.endsWith('m') ? Number(cpu.slice(0, -1)) / 1000 : Number(cpu)
  const bytes = Number(match[1]) * (units[match[2]] || 1)
  if (cores <= 0 || bytes <= 0 || !Number.isInteger(replicas) || replicas < 1) return undefined
  return { cpu: cores * replicas, memoryMiB: (bytes * replicas) / 1024 ** 2 }
}

export function runnerReservationLabel(resources: RunnerResources, replicas: number) {
  const value = runnerReservation(resources, replicas)
  if (!value) return 'Enter valid CPU and memory reservations to see the pool total.'
  return `${Number(value.cpu.toFixed(3))} CPU cores and ${Number(value.memoryMiB.toFixed(3))} MiB memory reserved across ${replicas} ${replicas === 1 ? 'slot' : 'slots'}.`
}
