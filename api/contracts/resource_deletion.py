route('/projects/{id}', 'delete', 'deleteEmptyProject', obj({'status': S}, ['status']), obj({'confirm_name': S}, ['confirm_name']))
route('/applications/{id}', 'delete', 'deleteEmptyApplication', obj({'status': S}, ['status']), obj({'confirm_name': S, 'expected_revision': I}, ['confirm_name', 'expected_revision']))
