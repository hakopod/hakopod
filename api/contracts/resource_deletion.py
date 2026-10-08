route('/projects/{id}', 'delete', 'deleteEmptyProject', obj({'status': S}, ['status']), obj({'confirm_name': S}, ['confirm_name']))
route('/applications/{id}', 'delete', 'deleteEmptyApplication', obj({'status': S}, ['status']), obj({'confirm_name': S, 'expected_revision': I}, ['confirm_name', 'expected_revision']))

route('/projects/{project}/environments/{environment}', 'delete', 'deleteEmptyEnvironment', obj({'status': S}, ['status']), obj({'confirm_name': S}, ['confirm_name']))
paths['/projects/{project}/environments/{environment}']['delete']['parameters'].extend([{'name': name, 'in': 'path', 'required': True, 'schema': S} for name in ['project', 'environment']])
