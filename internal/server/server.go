// Package server wires HTTP routes: the HTMX storefront (/), the admin dashboard (/admin)
// and the public JSON API (/api/v1). Handlers stay thin and delegate to service packages.
package server

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justinas/nosurf"
	"github.com/redis/go-redis/v9"

	"github.com/fuad71/ecommerce/internal/auth"
	"github.com/fuad71/ecommerce/internal/cache"
	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/config"
	"github.com/fuad71/ecommerce/internal/courier"
	"github.com/fuad71/ecommerce/internal/courier/steadfast"
	"github.com/fuad71/ecommerce/internal/media"
	"github.com/fuad71/ecommerce/internal/offers"
	"github.com/fuad71/ecommerce/internal/orders"
	"github.com/fuad71/ecommerce/internal/reports"
	"github.com/fuad71/ecommerce/internal/settings"
	"github.com/fuad71/ecommerce/web"
)

type Server struct {
	cfg      *config.Config
	pool     *pgxpool.Pool
	redis    *redis.Client
	media    *media.Store
	sessions *scs.SessionManager
	tmpl     *renderer

	auth     *auth.Service
	catalog  *catalog.Service
	orders   *orders.Service
	settings *settings.Service
	reports  *reports.Service
	offers   *offers.Service
	courier  *courier.Service
	cache    *cache.Cache
}

func New(cfg *config.Config, pool *pgxpool.Pool, rdb *redis.Client, ms *media.Store) (*Server, error) {
	tmpl, err := newRenderer()
	if err != nil {
		return nil, err
	}
	sm := scs.New()
	sm.Store = pgxstore.New(pool)
	sm.Lifetime = cfg.SessionLifetime
	sm.IdleTimeout = 2 * time.Hour
	sm.Cookie.Name = "admin_session"
	sm.Cookie.HttpOnly = true
	sm.Cookie.Secure = cfg.CookieSecure
	sm.Cookie.SameSite = http.SameSiteLaxMode
	sm.Cookie.Path = "/admin"

	st := settings.NewService(pool)
	ord := orders.NewService(pool, st)
	var sf *steadfast.Client
	if cfg.SteadfastConfigured() {
		sf = steadfast.New(cfg.SteadfastBaseURL, cfg.SteadfastAPIKey, cfg.SteadfastSecretKey, 20*time.Second)
	}
	return &Server{
		cfg: cfg, pool: pool, redis: rdb, media: ms, sessions: sm, tmpl: tmpl,
		auth:     auth.NewService(pool),
		catalog:  catalog.NewService(pool),
		orders:   ord,
		settings: st,
		reports:  reports.NewService(pool),
		offers:   offers.NewService(pool),
		courier:  courier.New(pool, ord, sf, cfg.SteadfastAutoSend),
		cache:    cache.New(rdb, cfg.CacheTTL),
	}, nil
}

// RunBackground starts background jobs (courier status sync) until ctx is cancelled.
func (s *Server) RunBackground(ctx context.Context) {
	go s.courier.RunSync(ctx, s.cfg.SteadfastSyncInterval)
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	if s.cfg.TrustProxy {
		// Only behind a proxy that sets these headers; otherwise clients could spoof their IP.
		r.Use(middleware.RealIP)
	}
	r.Use(requestLogger)
	r.Use(s.recoverer)
	r.Use(securityHeaders)
	r.Use(middleware.Compress(5, "text/html", "text/css", "application/javascript", "application/json"))
	r.NotFound(s.notFound)

	static, _ := fs.Sub(web.FS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", cacheControl(http.FileServer(http.FS(static)))))
	r.Get("/media/*", s.serveMedia)
	r.Get("/healthz", s.healthz)
	r.Get("/robots.txt", s.robots)
	r.Get("/sitemap.xml", s.h(s.sitemap))

	r.Route("/api/v1", s.apiRoutes)
	r.Get("/api/docs", s.h(s.apiDocs))
	r.Get("/api/openapi.yaml", s.openAPISpec)

	// HTML routes are CSRF-protected.
	r.Group(func(r chi.Router) {
		r.Use(s.csrf)

		r.Get("/", s.h(s.home))
		r.Get("/products", s.h(s.productList))
		r.Get("/category/{slug}", s.h(s.productList))
		r.Get("/offers", s.h(s.offerList))
		r.Get("/offers/{offer}", s.h(s.productList))
		r.Get("/product/{slug}", s.h(s.productDetail))
		r.Get("/cart", s.h(s.cartPage))
		r.Post("/cart/summary", s.h(s.cartSummary))
		r.Get("/checkout", s.h(s.checkoutPage))
		r.With(s.rateLimit("checkout", 10, time.Minute)).Post("/checkout", s.h(s.checkoutSubmit))
		r.Get("/order/{number}", s.h(s.orderConfirmation))
		r.Get("/track", s.h(s.trackPage))
		r.With(s.rateLimit("track", 15, time.Minute)).Post("/track", s.h(s.trackSubmit))
		r.Get("/policies", s.h(s.policies))

		r.Route("/admin", s.adminRoutes)
	})
	return r
}

func (s *Server) csrf(next http.Handler) http.Handler {
	h := nosurf.New(next)
	h.SetBaseCookie(http.Cookie{Path: "/", HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	h.SetIsTLSFunc(func(r *http.Request) bool {
		return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || strings.HasPrefix(s.cfg.AppURL, "https://")
	})
	h.SetFailureHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.WarnContext(r.Context(), "csrf check failed", "reason", nosurf.Reason(r), "path", r.URL.Path)
		s.render(w, r, http.StatusForbidden, "store/error", "layout", map[string]any{
			"Title": "Session expired", "Message": "Your session expired or the form was tampered with. Please go back, reload the page and try again.",
		})
	}))
	return h
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	checks := map[string]string{"database": "ok", "redis": "ok", "storage": "ok"}
	status := http.StatusOK
	if err := s.pool.Ping(ctx); err != nil {
		checks["database"], status = err.Error(), http.StatusServiceUnavailable
	}
	if err := s.redis.Ping(ctx).Err(); err != nil {
		checks["redis"], status = err.Error(), http.StatusServiceUnavailable
	}
	if err := s.media.Ping(ctx); err != nil {
		checks["storage"], status = err.Error(), http.StatusServiceUnavailable
	}
	writeJSON(w, status, checks)
}
