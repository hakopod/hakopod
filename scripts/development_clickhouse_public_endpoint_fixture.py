"""Source-backed ClickHouse fixture sizes shared by preflight and execution."""

GIB = 1024 ** 3
MIB = 1024 ** 2
DATA_CPU_MILLIS = 500
DATA_MEMORY_BYTES = 2 * GIB
KEEPER_CPU_MILLIS = 250
KEEPER_MEMORY_BYTES = 256 * MIB
PROBE_CPU_MILLIS = 250
PROBE_MEMORY_BYTES = 256 * MIB
SHAPES = {
    'standalone': {'mode': 'standalone', 'shards': 1, 'replicas': 0, 'members': 1, 'keepers': 0},
    'cluster': {'mode': 'cluster', 'shards': 2, 'replicas': 1, 'members': 4, 'keepers': 3},
    'maximum': {'mode': 'cluster', 'shards': 8, 'replicas': 5, 'members': 48, 'keepers': 3},
}


def fixture(shape):
    if not isinstance(shape, str) or shape not in SHAPES:
        raise ValueError('fixture_shape must be standalone, cluster or maximum')
    result = dict(SHAPES[shape], shape=shape)
    result.update(data_cpu_millis=DATA_CPU_MILLIS, data_memory_bytes=DATA_MEMORY_BYTES,
                  keeper_cpu_millis=KEEPER_CPU_MILLIS, keeper_memory_bytes=KEEPER_MEMORY_BYTES,
                  probe_cpu_millis=PROBE_CPU_MILLIS, probe_memory_bytes=PROBE_MEMORY_BYTES,
                  surge_members=1, surge_keepers=1 if result['keepers'] else 0,
                  data_storage_gib=1, keeper_storage_gib=1)
    # Fixture targets run sequentially. Migration and member-loss can overlap one
    # replacement data pod and one replacement Keeper, never a second database.
    result['peak_cpu_millis'] = ((result['members'] + 1) * DATA_CPU_MILLIS +
                                (result['keepers'] + result['surge_keepers']) * KEEPER_CPU_MILLIS)
    result['peak_memory_bytes'] = ((result['members'] + 1) * DATA_MEMORY_BYTES +
                                  (result['keepers'] + result['surge_keepers']) * KEEPER_MEMORY_BYTES)
    result['pvc_bytes'] = (2 * result['members'] + result['keepers']) * GIB
    result['scratch_free_bytes_required'] = 16 * GIB + result['pvc_bytes']
    return result


def database_spec(shape, run_id, nodes):
    shape = fixture(shape)
    return {'schema_version': 1, 'name': run_id, 'engine': 'clickhouse', 'version': '26.3',
            'mode': shape['mode'], 'replicas': shape['replicas'], 'shards': shape['shards'],
            'cpu': '500m', 'memory': '2Gi', 'storage_gib': shape['data_storage_gib'],
            'placement': {'node_names': list(nodes)}, 'tls': {'mode': 'required'}}


def node_peak(shape):
    shape = fixture(shape)
    return {'cpu_millis': shape['peak_cpu_millis'] + 2 * PROBE_CPU_MILLIS,
            'memory_bytes': shape['peak_memory_bytes'] + 2 * PROBE_MEMORY_BYTES,
            'members': shape['members'], 'keepers': shape['keepers'],
            'surge_members': shape['surge_members'], 'surge_keepers': shape['surge_keepers'],
            'probes': 2}


def plan_peak(nodes, shape):
    # There is no per-member node pin in the public database spec. Reserve the
    # entire peak on each eligible node rather than invent a scheduler outcome.
    expected = node_peak(shape)
    for node in nodes.values():
        remaining = node['remaining_before_plan']
        if any(expected[key] > remaining[key] for key in ('cpu_millis', 'memory_bytes')):
            raise ValueError('each eligible node must fit the complete ClickHouse fixture, Keeper and replacement peak')
    return {name: expected.copy() for name in nodes}
