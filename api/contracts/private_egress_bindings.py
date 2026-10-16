"""Administrator-approved private egress destinations (database-managed).

Writes are installation administrator only and validated together with the
read-only HAKOPOD_PRIVATE_EGRESS_FILE bindings. Deployment readers can list the
bindings for their own project and environment.
"""

I64 = {'type': 'integer', 'format': 'int64'}
BINDING = {'name': S, 'project': S, 'environment': S, 'application': S,
           'services': array(S), 'cidrs': array(S), 'ports': array(I)}
schemas['PrivateEgressBinding'] = obj(BINDING, ['name', 'project', 'environment', 'application', 'services', 'cidrs', 'ports'])
schemas['PrivateEgressBindingRecord'] = obj({'name': S, 'revision': I64, 'config': ref('PrivateEgressBinding'),
                                             'created_by': S, 'created_at': T, 'updated_at': T},
                                            ['name', 'revision', 'config', 'created_by', 'created_at', 'updated_at'])
schemas['PrivateEgressBindingInput'] = obj({**BINDING, 'expected_revision': {**I64, 'minimum': 0}},
                                           ['project', 'environment', 'application', 'services', 'cidrs', 'ports', 'expected_revision'])
schemas['ScopedPrivateEgressBinding'] = obj({**BINDING, 'managed_by': {'type': 'string', 'enum': ['file', 'database']}},
                                            ['name', 'project', 'environment', 'application', 'services', 'cidrs', 'ports', 'managed_by'])

NAME_PARAM = [{'name': 'name', 'in': 'path', 'required': True, 'schema': S}]
def ok(schema): return {'description': 'Successful response', 'content': {'application/json': {'schema': schema}}}

paths['/admin/private-egress-bindings'] = {'get': {
    'operationId': 'listPrivateEgressBindings',
    'summary': 'List database and file private egress bindings (administrator only)',
    'responses': {'200': ok(obj({'items': array(ref('PrivateEgressBindingRecord')),
                                 'managed_by_file': array(ref('PrivateEgressBinding'))}, ['items', 'managed_by_file']))}}}
paths['/admin/private-egress-bindings/{name}'] = {
    'get': {'operationId': 'getPrivateEgressBinding', 'summary': 'Read one database private egress binding (administrator only)',
            'parameters': NAME_PARAM, 'responses': {'200': ok(ref('PrivateEgressBindingRecord'))}},
    'put': {'operationId': 'putPrivateEgressBinding', 'summary': 'Create or update a private egress binding (administrator only)',
            'parameters': NAME_PARAM,
            'requestBody': {'required': True, 'content': {'application/json': {'schema': ref('PrivateEgressBindingInput')}}},
            'responses': {'200': ok(ref('PrivateEgressBindingRecord')), '201': ok(ref('PrivateEgressBindingRecord')),
                          '409': {'description': 'Revision conflict'}}},
    'delete': {'operationId': 'deletePrivateEgressBinding', 'summary': 'Revoke a private egress binding (administrator only)',
               'parameters': NAME_PARAM,
               'requestBody': {'required': True, 'content': {'application/json': {'schema': obj({'expected_revision': {**I64, 'minimum': 1}}, ['expected_revision'])}}},
               'responses': {'200': ok(obj({'deleted': B}, ['deleted'])), '409': {'description': 'Revision conflict'}}}}
paths['/private-egress-bindings'] = {'get': {
    'operationId': 'listScopedPrivateEgressBindings',
    'summary': 'List approved private egress destinations for a project and environment',
    'parameters': [{'name': n, 'in': 'query', 'required': False, 'schema': S} for n in ('project', 'environment', 'application')],
    'responses': {'200': ok(obj({'items': array(ref('ScopedPrivateEgressBinding'))}, ['items']))}}}
