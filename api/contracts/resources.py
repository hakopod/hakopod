"""Per-service CPU/memory budgets; omitted values inherit size."""
schemas["Resources"] = obj({
    key: {"type": "string", "maxLength": 32, "description": help}
    for key, help in {
        "cpu_request": "Per-replica CPU reservation, 1m–64 cores; omit to inherit size.",
        "cpu_limit": "Per-replica CPU limit, 1m–64 cores; must cover request.",
        "memory_request": "Per-replica memory reservation, 1Mi–256Gi; omit to inherit size.",
        "memory_limit": "Per-replica memory limit, 1Mi–256Gi; must cover request.",
    }.items()
})
schemas["Resources"]["additionalProperties"] = False
schemas["Service"]["properties"]["resources"] = ref("Resources")
