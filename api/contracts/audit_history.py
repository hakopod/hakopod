route('/audit/history', 'get', 'listUserAuditHistory', obj({'items': array(ref('Audit')), 'next_cursor': I}, ['items', 'next_cursor']))
route('/audit/export', 'get', 'exportUserAuditHistory', S)
for endpoint in ('/audit/history', '/audit/export'):
    paths[endpoint]['get']['parameters'] = [
        {'name': 'identity_id', 'in': 'query', 'required': True, 'schema': {'type': 'string', 'pattern': '^[a-f0-9]{32}$'}},
        {'name': 'before', 'in': 'query', 'schema': {'type': 'integer', 'minimum': 1}},
    ]
paths['/audit/export']['get']['responses']['200']['content'] = {'text/csv': {'schema': S}}
paths['/audit/export']['get']['responses']['200']['headers'] = {'X-Hakopod-Next-Cursor': {'description': 'Use as before to export the next page; each export contains at most 1000 events.', 'schema': I}}
