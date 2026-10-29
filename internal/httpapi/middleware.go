package httpapi

import "context"

// ctxKey keys the request-scoped values httpapi adds. The request id and the
// cross-cutting middleware live in internal/platform/httpserver.
type ctxKey int

const ctxIfMatchVersion ctxKey = iota

// IfMatchVersion returns the version N of the validated `If-Match: "<prefix>-N"`
// header. It is only meaningful in handlers of operations that require If-Match
// (the request guard rejects the request before the handler otherwise).
func IfMatchVersion(ctx context.Context) int {
	v, _ := ctx.Value(ctxIfMatchVersion).(int)
	return v
}
