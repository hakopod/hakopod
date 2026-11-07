package cluster

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// TransientAPIError reports whether a short retry can clear err: client-side
// throttling, request or server timeouts, 429/5xx responses and dropped or
// refused connections. Cancellation is not transient; the caller stopped.
func TransientAPIError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if ThrottledAPIError(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if apierrors.IsTooManyRequests(err) || apierrors.IsServerTimeout(err) || apierrors.IsTimeout(err) || apierrors.IsServiceUnavailable(err) || apierrors.IsInternalError(err) {
		return true
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		code := status.Status().Code
		return code == 429 || code >= 500
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// ThrottledAPIError reports whether client-go's own rate limiter refused to
// wait for a request slot within the caller's deadline. golang.org/x/time/rate
// returns an unexported error type, so only the message identifies it.
func ThrottledAPIError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "client rate limiter Wait returned an error") || strings.Contains(message, "rate: Wait(n=")
}
