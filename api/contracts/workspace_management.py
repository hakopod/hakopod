# Scoped management, display names and build runtime overrides.

schemas['Application']["properties"].update({'display_name': {'type': 'string'},
 'service_display_names': {'type': 'object',
                           'additionalProperties': {'type': 'string'}},
 'metadata_revision': {'type': 'integer', 'format': 'int64'}})

schemas['Project']["properties"].update({'metadata_revision': {'type': 'integer', 'format': 'int64'}})

schemas['Principal']["properties"].update({'can_manage_git': {'type': 'boolean'},
 'can_manage_applications': {'type': 'boolean'}})

schemas['BuildConfig']["properties"].update({'command': {'type': 'array',
             'items': {'type': 'string'},
             'description': 'Exec-form runtime override. Omit to preserve the '
                            'existing service setting; empty array uses the '
                            'image default.'},
 'args': {'type': 'array',
          'items': {'type': 'string'},
          'description': 'Exec-form runtime override. Omit to preserve the '
                         'existing service setting; empty array uses the image '
                         'default.'}})

schemas['BuildInput']["properties"].update({'command': {'type': 'array',
             'items': {'type': 'string'},
             'description': 'Exec-form runtime override. Omit to preserve the '
                            'existing service setting; empty array uses the '
                            'image default.'},
 'args': {'type': 'array',
          'items': {'type': 'string'},
          'description': 'Exec-form runtime override. Omit to preserve the '
                         'existing service setting; empty array uses the image '
                         'default.'}})

paths['/applications/{id}/name'] = {'put': {'operationId': 'renameApplication',
         'parameters': [{'name': 'id',
                         'in': 'path',
                         'required': True,
                         'schema': {'type': 'string'}}],
         'requestBody': {'required': True,
                         'content': {'application/json': {'schema': {'type': 'object',
                                                                     'required': ['display_name',
                                                                                  'expected_metadata_revision'],
                                                                     'properties': {'display_name': {'type': 'string'},
                                                                                    'expected_metadata_revision': {'type': 'integer',
                                                                                                                   'format': 'int64'}}}}}},
         'responses': {'200': {'description': 'Name saved',
                               'content': {'application/json': {'schema': {'$ref': '#/components/schemas/Application'}}}}}}}

paths['/applications/{id}/services/{service}/name'] = {'put': {'operationId': 'renameService',
         'parameters': [{'name': 'id',
                         'in': 'path',
                         'required': True,
                         'schema': {'type': 'string'}},
                        {'name': 'service',
                         'in': 'path',
                         'required': True,
                         'schema': {'type': 'string'}}],
         'requestBody': {'required': True,
                         'content': {'application/json': {'schema': {'type': 'object',
                                                                     'required': ['display_name',
                                                                                  'expected_metadata_revision'],
                                                                     'properties': {'display_name': {'type': 'string'},
                                                                                    'expected_metadata_revision': {'type': 'integer',
                                                                                                                   'format': 'int64'}}}}}},
         'responses': {'200': {'description': 'Name saved',
                               'content': {'application/json': {'schema': {'$ref': '#/components/schemas/Application'}}}}}}}

paths['/projects/{id}/name'] = {'put': {'operationId': 'renameProject',
         'parameters': [{'name': 'id',
                         'in': 'path',
                         'required': True,
                         'schema': {'type': 'string'}}],
         'requestBody': {'required': True,
                         'content': {'application/json': {'schema': {'type': 'object',
                                                                     'required': ['display_name',
                                                                                  'expected_metadata_revision'],
                                                                     'properties': {'display_name': {'type': 'string'},
                                                                                    'expected_metadata_revision': {'type': 'integer',
                                                                                                                   'format': 'int64'}}}}}},
         'responses': {'200': {'description': 'Name saved',
                               'content': {'application/json': {'schema': {'type': 'object',
                                                                           'properties': {'status': {'type': 'string'}}}}}}}}}

for name in ["BuildConfig", "BuildInput"]:
    schemas[name]["properties"]["env"] = {"type":"object", "additionalProperties":S, "maxProperties":128, "description":"Runtime plain variables. Omit to preserve existing service variables; an explicit object replaces them. Values are not exposed to build steps. Use application secret references for credentials."}
