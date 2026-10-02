package cluster

// ManagedPlatformRuntimeError preserves a fixed, secret-safe reconciliation
// stage while retaining the underlying cause for errors.Is and errors.As.
type ManagedPlatformRuntimeError struct {
	Category string
	Err      error
}

func (e *ManagedPlatformRuntimeError) Error() string {
	safe := map[string]string{
		"capacity_admission":                   "managed platform reconciliation capacity admission failed",
		"neon_snapshot_mismatch":               "Neon runtime snapshot does not match the durable operation",
		"neon_snapshot_missing":                "Neon runtime snapshot is unavailable",
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
