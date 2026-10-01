export function keyLifetimeOptions(cloud: boolean) {
  return [
    { value: '7', label: '7 days' },
    { value: '30', label: '30 days' },
    { value: '60', label: '60 days' },
    { value: '89', label: '89 days' },
    ...(cloud ? [{ value: 'never', label: 'Never expires' }] : []),
  ]
}

export function keyLifetimeInput(value: string, cloud: boolean, now = Date.now()) {
  if (!keyLifetimeOptions(cloud).some((option) => option.value === value)) {
    throw new Error('Choose a supported key lifetime.')
  }
  if (value === 'never') return { never_expires: true as const }
  return { expires_at: new Date(now + Number(value) * 86400000).toISOString() }
}
