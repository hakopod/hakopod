package cluster

// ManagedPlatformRuntimeError preserves a fixed, secret-safe reconciliation
// stage while retaining the underlying cause for errors.Is and errors.As.
type ManagedPlatformRuntimeError struct {
	Category string
	Err      error
}

func (e *ManagedPlatformRuntimeError) Error() string {
	safe := map[string]string{
		"platform_identity":                    "managed platform certificate reconciliation failed",
		"platform_tls_observe":                 "managed platform served certificate verification is pending",
		"capacity_admission":                   "managed platform reconciliation capacity admission failed",
		"neon_snapshot_mismatch":               "Neon runtime snapshot does not match the durable operation",
		"neon_snapshot_missing":                "Neon runtime snapshot is unavailable",
		"neon_node_inventory":                  "Neon placement identity is unavailable",
		"neon_recovery_binding":                "Neon recovery binding could not be loaded",
		"neon_claims":                          "Neon durable claims could not be loaded",
		"neon_namespace":                       "Neon namespace reconciliation failed",
		"neon_runtime_repair":                  "Neon runtime repair failed",
		"neon_tls_prepare":                     "Neon certificate preparation failed",
		"neon_lifecycle_prepare":               "Neon lifecycle configuration is invalid",
		"neon_controller_secret":               "Neon controller credentials could not be prepared",
		"neon_render":                          "Neon manifest rendering failed",
		"neon_secret_validate":                 "Neon secret snapshot validation failed",
		"neon_secret_apply":                    "Neon secret reconciliation failed",
		"neon_object_apply":                    "Neon resource reconciliation failed",
		"neon_bootstrap_observe":               "Neon bootstrap observation failed",
		"neon_lifecycle_provision":             "Neon lifecycle provisioning is pending",
		"neon_compute_replay":                  "Neon compute restart recovery is pending",
		"neon_serving_observe":                 "Neon serving observation failed",
		"neon_snapshot_prune":                  "Neon snapshot cleanup failed",
		"neon_recovery_observe":                "Neon recovery observation failed",
		"neon_proxy_activate":                  "Neon proxy route activation failed",
		"neon_lifecycle_deprovision":           "Neon lifecycle removal is pending",
		"runtime_unavailable":                  "managed platform cluster runtime is unavailable",
		"snapshot_invalid":                     "managed platform snapshot is invalid",
		"supabase_assets_invalid":              "Supabase pinned assets are invalid",
		"supabase_apply_configmap":             "Supabase ConfigMap reconciliation failed",
		"supabase_apply_deployment":            "Supabase Deployment reconciliation failed",
		"supabase_apply_networkpolicy":         "Supabase NetworkPolicy reconciliation failed",
		"supabase_apply_pvc":                   "Supabase persistent-volume claim reconciliation failed",
		"supabase_apply_secret":                "Supabase Secret reconciliation failed",
		"supabase_apply_service":               "Supabase Service reconciliation failed",
		"supabase_apply_statefulset":           "Supabase StatefulSet reconciliation failed",
		"supabase_claims":                      "Supabase durable claim loading failed",
		"supabase_database_tls_validation":     "Supabase database TLS validation failed",
		"supabase_database_url_validation":     "Supabase database URL validation failed",
		"supabase_gateway_validation":          "Supabase gateway validation failed",
		"supabase_namespace":                   "Supabase namespace reconciliation failed",
		"supabase_observe":                     "Supabase runtime observation failed",
		"supabase_qualification":               "Supabase operator qualification changed",
		"supabase_runtime_secret_validation":   "Supabase runtime secret validation failed",
		"neon_qualification":                   "Neon operator qualification changed",
		"supabase_render":                      "Supabase manifest rendering failed",
		"supabase_rotate_database_credentials": "Supabase database credential rotation failed",
		"supabase_snapshot_mismatch":           "Supabase runtime snapshot does not match the durable operation",
		"supabase_snapshot_missing":            "Supabase runtime snapshot is unavailable",
		"unsupported_kind":                     "managed platform kind is not supported by this runtime",
	}
	if message, ok := safe[e.Category]; ok {
		return message
	}
	return "managed platform reconciliation failed"
}

func (e *ManagedPlatformRuntimeError) Unwrap() error { return e.Err }

func (e *ManagedPlatformRuntimeError) SafeCategory() string { return e.Category }

func managedPlatformRuntimeError(category string, err error) error {
	if err == nil {
		return nil
	}
	return &ManagedPlatformRuntimeError{Category: category, Err: err}
}
