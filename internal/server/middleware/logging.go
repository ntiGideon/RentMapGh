package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// AccessLog writes one structured line per request.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)

		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		slog.LogAttrs(r.Context(), level, "http",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.Int("bytes", ww.BytesWritten()),
			slog.Duration("dur", time.Since(start)),
			slog.String("ip", r.RemoteAddr),
			slog.Bool("htmx", r.Header.Get("HX-Request") == "true"),
			slog.String("req_id", chimw.GetReqID(r.Context())),
		)
	})
}

// Recover turns panics into a logged error and a 500 page rendered by onPanic.
func Recover(onPanic http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler { // deliberate abort: let net/http handle it
					panic(rec)
				}
				slog.ErrorContext(r.Context(), "panic",
					"err", fmt.Sprint(rec),
					"stack", string(debug.Stack()),
					"req_id", chimw.GetReqID(r.Context()),
				)
				onPanic(w, r)
			}()
			next.ServeHTTP(w, r)
		})
	}
}
