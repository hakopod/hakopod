schemas['DeploymentJob'] = obj({'timeout_seconds': {'type': 'integer', 'minimum': 10, 'maximum': 900}, 'retries': {'type': 'integer', 'minimum': 0, 'maximum': 3}})
schemas['ConfigurationFile'] = obj({'mount_path': S, 'content': S, 'secret': ref('SecretRef'), 'mode': {'type': 'integer', 'enum': [288, 292]}}, ['mount_path'])
schemas['ServiceBinding'] = obj({'service': S, 'protocol': {'type': 'string', 'enum': ['http','postgres','mysql','redis']}, 'database': S, 'username': S, 'password': ref('SecretRef')}, ['service','protocol'])
schemas['Service']['properties'].update({'job': ref('DeploymentJob'), 'files': mapping(ref('ConfigurationFile')), 'bindings': mapping(ref('ServiceBinding'))})

schemas['HTTPEndpoint'] = obj({'port': I, 'domain': S}, ['port'])
schemas['Service']['properties']['http'] = mapping(ref('HTTPEndpoint'))
schemas['Service']['properties']['backend_http2'] = {**B, "description": "Opt-in: the ingress speaks HTTP/2 (h2) to this service's backend, for gRPC and other h2-only servers. Requires public with a port. Rejected with serverless or with named http endpoints."}
schemas['ServiceStatus']['properties']['endpoints'] = mapping(S)

schemas['BuildConfig']['properties']['build_args'] = mapping(S)
schemas['BuildInput']['properties']['build_args'] = mapping(S)
