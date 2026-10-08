//go:build !hakopod_native_acceptance

package go_ora

type nativeDiagnosticState struct{}

func (*defaultStmt) nativeBoundary(string, int, *ParameterInfo) {}

func (*defaultStmt) nativeTrailerStart()    {}
func (*defaultStmt) nativeTrailerRead(bool) {}
func (*defaultStmt) nativeTrailerEnd()      {}

func (*defaultStmt) nativeResponseFailure(error) {}
