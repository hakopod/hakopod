package managedplatform

func neonPageserverRegistrationBody(node NeonPageserverRegistration) map[string]any {
	return map[string]any{
		"node_id": node.NodeID, "listen_pg_addr": node.Host, "listen_pg_port": 6400,
		"listen_grpc_addr": nil, "listen_grpc_port": nil,
		"listen_http_addr": node.Host, "listen_http_port": 9897, "listen_https_port": 9898,
		"availability_zone_id": node.AvailabilityZone, "node_ip_addr": nil,
	}
}

func neonSafekeeperRegistrationBody(node NeonSafekeeperRegistration) map[string]any {
	return map[string]any{
		"id": node.NodeID, "region_id": "hakopod", "version": node.Generation,
		"host": node.Host, "port": 5454, "active": true, "http_port": 7677,
		"https_port": 7676, "availability_zone_id": node.AvailabilityZone,
	}
}
