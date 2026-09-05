package sandbox

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// LogUnary and LogStream say what the sandbox's servers were asked and what
// they answered, one line per call, in the browser's console.
//
// The real server has no such line: what a deployment keeps of its calls is
// the trail, and a request log beside it would be a second record of the
// same writes with none of the reads' reasons. The sandbox is different --
// its whole point is somebody at a page watching what the page does, and a
// call that vanished into a Worker with no line anywhere was the first thing
// that somebody asked about. Debug level rather than info, so the line is
// there for the console and not for a log file.
func LogUnary(l *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		start := time.Now()
		res, err := next(ctx, req)
		l.DebugContext(ctx, "rpc",
			slog.String("method", info.FullMethod),
			slog.String("code", status.Code(err).String()),
			slog.Duration("took", time.Since(start).Round(time.Microsecond)),
		)

		return res, err
	}
}

// LogStream is the same, for the watch streams; the line is written when the
// stream ends, which for a watch is when the page lets go of it.
func LogStream(l *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		start := time.Now()
		err := next(srv, ss)
		l.DebugContext(ss.Context(), "rpc",
			slog.String("method", info.FullMethod),
			slog.String("code", status.Code(err).String()),
			slog.Duration("open", time.Since(start).Round(time.Millisecond)),
			slog.Bool("stream", true),
		)

		return err
	}
}
