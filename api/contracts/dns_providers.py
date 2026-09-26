"""Stored DNS provider credentials, and the records hakopod can create with them.

A provider's zone filter, not the token, is the boundary hakopod can enforce: the
platform cannot see what a provider token actually reaches. Managing a provider is
installation administrators and delegated project owners; machine keys are
refused. Creating records is not verifying them.
"""

I64 = {'type': 'integer', 'format': 'int64'}
NAME = {'type': 'string', 'maxLength': 80}
KIND = {'type': 'string', 'enum': ['cloudflare']}
ZONES = {'type': 'array', 'minItems': 1, 'maxItems': 32, 'items': S,
         'description': 'DNS zones this credential may write to. hakopod cannot see what a '
                        'provider token really reaches, so this list is the enforceable '
                        'boundary. Widening it requires re-entering the token.'}

schemas['DNSProvider'] = obj({
    'id': S, 'name': NAME, 'kind': KIND,
    'project': S, 'environment': S,
    'zone_filter': ZONES,
    'revision': {**I64, 'readOnly': True},
    'enabled': B,
    'created_at': {'type': 'string', 'format': 'date-time', 'readOnly': True},
    'updated_at': {'type': 'string', 'format': 'date-time', 'readOnly': True},
}, ['id', 'name', 'kind', 'zone_filter', 'revision', 'enabled', 'created_at', 'updated_at'])
schemas['DNSProvider']['description'] = ('A provider is installation-wide, or scoped to one '
                                         'project and one environment, never to a project alone. '
                                         'Credentials are never returned.')

schemas['DNSProviderSummary'] = obj({'id': S, 'name': NAME, 'kind': KIND}, ['name', 'kind'])
schemas['DNSProviderSummary']['description'] = ('What a non-administrator sees: name and kind '
                                                'only, never the zone filter. The application '
                                                'listing adds the id the record endpoint takes.')

schemas['DNSProviderInput'] = obj({
    'name': NAME, 'kind': KIND, 'project': S, 'environment': S,
    'zone_filter': ZONES, 'enabled': B,
    'expected_revision': {**I64, 'minimum': 0,
                          'description': 'Zero creates; the current revision updates.'},
    'credentials': {'type': 'object', 'additionalProperties': False, 'writeOnly': True,
                    'properties': {'token': {'type': 'string', 'writeOnly': True,
                                             'maxLength': 8192}},
                    'required': ['token'],
                    'description': 'Required on create. Omit on update to keep the stored token; '
                                   'a changed name, kind or zone filter requires it again.'},
}, ['kind', 'zone_filter', 'expected_revision'])

_name = [{'name': 'name', 'in': 'path', 'required': True, 'schema': NAME}]
_error = {'description': 'Error', 'content': {'application/json': {'schema': ref('Error')}}}


def _json(schema, description='Success'):
    return {'description': description, 'content': {'application/json': {'schema': schema}}}


paths['/dns-providers'] = {'get': {
    'operationId': 'listDNSProviders',
    'summary': 'List DNS providers: full rows for administrators, names and kinds for a delegated '
               'project owner',
    'parameters': [{'name': v, 'in': 'query', 'required': False, 'schema': S}
                   for v in ['project', 'environment']],
    'responses': {'200': _json(obj({'items': array({'oneOf': [ref('DNSProvider'),
                                                              ref('DNSProviderSummary')]})},
                                   ['items'])),
                  'default': _error}}}

paths['/dns-providers/{name}'] = {
    'put': {'operationId': 'putDNSProvider',
            'summary': 'Create or update a DNS provider credential',
            'parameters': _name,
            'requestBody': {'required': True,
                            'content': {'application/json': {'schema': ref('DNSProviderInput')}}},
            'responses': {'200': _json(ref('DNSProvider')),
                          '201': _json(ref('DNSProvider'), 'Created'),
                          '409': {'description': 'The revision changed'},
                          'default': _error}},
    'delete': {'operationId': 'deleteDNSProvider',
               'summary': 'Delete a DNS provider credential that owns no records',
               'parameters': _name,
               'requestBody': {'required': True, 'content': {'application/json': {'schema': obj(
                   {'project': S, 'environment': S, 'expected_revision': {**I64, 'minimum': 1}},
                   ['expected_revision'])}}},
               'responses': {'200': _json(obj({'deleted': B}, ['deleted'])),
                             '409': {'description': 'The provider still owns records, or its '
                                                    'revision changed'},
                             'default': _error}}}

schemas['DNSRecord'] = obj({'type': {'type': 'string', 'enum': ['TXT', 'CNAME']},
                            'name': S, 'value': S}, ['type', 'name', 'value'])

schemas['DNSRecordResult'] = obj({
    'hostname': S,
    'status': {'type': 'string', 'enum': ['created', 'exists', 'conflict', 'failed', 'skipped'],
               'description': 'created: written now. exists: identical records were already '
                              'there. conflict: a different value occupies the name and '
                              'replace_existing was false. failed: the provider refused or did '
                              'not answer. skipped: not attempted, for a hostname this '
                              'application is not awaiting verification for, or one left when '
                              'the request ran out of time.'},
    'message': S,
    'records': array(ref('DNSRecord')),
}, ['hostname', 'status', 'message', 'records'])
schemas['DNSRecordResult']['description'] = ('One hostname\'s outcome. Records reaching a '
                                             'provider is not verification: the domain stays '
                                             'awaiting DNS until the verify step confirms it.')

schemas['DNSRecordsInput'] = obj({
    'provider_id': S,
    'hostnames': {'type': 'array', 'minItems': 1, 'maxItems': 20, 'items': S,
                  'description': 'Hostnames this application is already awaiting or holding '
                                 'verification for.'},
    'replace_existing': {**B, 'description': 'Overwrite a different value already at the record '
                                             'name. Without it such a name is reported as a '
                                             'conflict and left alone.'},
}, ['provider_id', 'hostnames'])
