package httpserver

import (
	"context"
	"errors"
	"log/slog"

	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

// Op wraps a handler so that its error is answered as a problem, the way
// httpapi's response error handler did before the migration. A request the
// client abandoned answers 499 client_closed_request and is logged at DEBUG;
// an unexpected error is logged at ERROR with the underlying error and the
// request id, and the client only gets the generic 500 problem.
//
// Handlers must be registered through Op (huma.Register(api, op, Op(log, fn))).
func Op[I, O any](log *slog.Logger, fn func(context.Context, *I) (*O, error)) func(context.Context, *I) (*O, error) {
	return func(ctx context.Context, in *I) (*O, error) {
		out, err := fn(ctx, in)
		if err == nil {
			return out, nil
		}
		requestID := RequestIDFrom(ctx)
		if errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled) {
			// Nobody reads the answer, so keep it out of the error log.
			log.DebugContext(ctx, "request canceled by the client", "request_id", requestID)
			return nil, problem.New(statusClientClosedRequest, "client_closed_request", "Client closed request",
				"The client closed the request before the answer was ready.")
		}
		var existing *problem.Problem
		if errors.As(err, &existing) {
			return nil, existing // a handler already answered with a problem
		}
		p, unexpected := problem.From(err, requestID)
		if unexpected {
			log.ErrorContext(ctx, "unhandled error", "err", err, "request_id", p.RequestID)
		}
		return nil, p
	}
}
