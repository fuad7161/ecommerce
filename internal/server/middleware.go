package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/fuad71/ecommerce/internal/auth"
	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/orders"
)

// h adapts handlers that return an error. Known "not found" errors become 404 pages;
// anything else is logged and rendered as a 500.
func (s *Server) h(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			if errors.Is(err, catalog.ErrNotFound) || errors.Is(err, orders.ErrNotFound) {
				s.notFound(w, r)
				return
			}
			s.serverError(w, r, err)
		}
	}
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "request failed", "err", err, "path", r.URL.Path,
		"request_id", middleware.GetReqID(r.Context()))
	if isHTMX(r) {
		w.Header().Set("HX-Reswap", "none")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprint(w, `<!doctype html><title>Error</title><div style="font-family:sans-serif;padding:3rem;text-align:center">
		<h1>Something went wrong</h1><p>Please try again in a moment.</p><a href="/">Go home</a></div>`)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	page := "store/error"
	s.render(w, r, http.StatusNotFound, page, "layout", map[string]any{
		"Title": "Page not found", "Message": "We couldn't find what you were looking for.",
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				slog.ErrorContext(r.Context(), "panic", "panic", rec, "stack", string(debug.Stack()))
				s.serverError(w, r, fmt.Errorf("panic: %v", rec))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if r.URL.Path == "/healthz" {
			return
		}
		slog.InfoContext(r.Context(), "http",
			"method", r.Method, "path", r.URL.Path, "status", ww.Status(), "bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(), "ip", r.RemoteAddr,
			"request_id", middleware.GetReqID(r.Context()))
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

// rateLimit is a fixed-window limiter per client IP backed by Redis.
// If Redis is unavailable it fails open so checkout keeps working.
func (s *Server) rateLimit(name string, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := fmt.Sprintf("rl:%s:%s:%d", name, r.RemoteAddr, time.Now().Unix()/int64(window.Seconds()))
			ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
			defer cancel()
			n, err := s.redis.Incr(ctx, key).Result()
			if err == nil && n == 1 {
				s.redis.Expire(ctx, key, window)
			}
			if err != nil {
				slog.WarnContext(r.Context(), "rate limiter unavailable", "err", err)
			} else if n > int64(limit) {
				w.Header().Set("Retry-After", fmt.Sprint(int(window.Seconds())))
				if isAPI(r) {
					writeJSON(w, http.StatusTooManyRequests, apiError{Error: "too many requests, please slow down"})
					return
				}
				s.render(w, r, http.StatusTooManyRequests, "store/error", "layout", map[string]any{
					"Title": "Too many requests", "Message": "You're doing that too often. Please wait a minute and try again.",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- Admin auth ----------

type ctxKey int

const adminKey ctxKey = 1

func adminFrom(r *http.Request) *auth.Admin {
	a, _ := r.Context().Value(adminKey).(*auth.Admin)
	return a
}

// loadAdmin resolves the logged-in admin on every request, so deactivated accounts lose access immediately.
func (s *Server) loadAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := s.sessions.GetInt64(r.Context(), "admin_id"); id > 0 {
			if a, err := s.auth.Get(r.Context(), id); err == nil && a.Active {
				r = r.WithContext(context.WithValue(r.Context(), adminKey, a))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if adminFrom(r) == nil {
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", "/admin/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requirePerm(p auth.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !adminFrom(r).Can(p) {
				s.render(w, r, http.StatusForbidden, "admin/forbidden", "layout", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"
}

func isAPI(r *http.Request) bool { return len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/api" }
