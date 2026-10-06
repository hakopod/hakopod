package cluster

// Change only after the pinned scoped operator and database image pass the
// named development cluster lifecycle, security, recovery and cleanup cases.
// Native package tests call the same private implementation while this stays
// closed; production API admission has no environment override.
const oracleFreeReleaseQualified = false
