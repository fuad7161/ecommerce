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

	"github.com/fuad71/ecommerce/internal/accounts"
	"github.com/fuad71/ecommerce/internal/auth"
	"github.com/fuad71/ecommerce/internal/cache"
	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/config"
	"github.com/fuad71/ecommerce/internal/courier"
	"github.com/fuad71/ecommerce/internal/courier/steadfast"
	"github.com/fuad71/ecommerce/internal/media"
	"github.com/fuad71/ecommerce/internal/offers"
	"github.com/fuad71/ecommerce/internal/orders"
	"github.com/fuad71/ecommerce/internal/payment"
	"github.com/fuad71/ecommerce/internal/payment/sslcommerz"
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
	// customerSessions is separate from the admin session (own cookie, own lifetime), so a
	// customer login can never carry admin rights and vice versa.
	customerSessions *scs.SessionManager
	tmpl             *renderer

	accounts *accounts.Service

	auth     *auth.Service
	catalog  *catalog.Service
	orders   *orders.Service
	settings *settings.Service
	reports  *reports.Service
	offers   *offers.Service
	courier  *courier.Service
	payments *payment.Service
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

	cs := scs.New()
	cs.Store = pgxstore.New(pool)
	cs.Lifetime = 30 * 24 * time.Hour
	cs.IdleTimeout = 14 * 24 * time.Hour
	cs.Cookie.Name = "customer_session"
	cs.Cookie.HttpOnly = true
	cs.Cookie.Secure = cfg.CookieSecure
	cs.Cookie.SameSite = http.SameSiteLaxMode
	cs.Cookie.Path = "/"
	cs.Cookie.Persist = true

	st := settings.NewService(pool)
	ord := orders.NewService(pool, st)
	var sf *steadfast.Client
	if cfg.SteadfastConfigured() {
		sf = steadfast.New(cfg.SteadfastBaseURL, cfg.SteadfastAPIKey, cfg.SteadfastSecretKey, 20*time.Second)
	}
	var gw *sslcommerz.Client
	if cfg.SSLCommerzConfigured() {
		base := sslcommerz.LiveBaseURL
		if cfg.SSLCommerzSandbox {
			base = sslcommerz.SandboxBaseURL
		}
		gw = sslcommerz.New(base, cfg.SSLCommerzStoreID, cfg.SSLCommerzStorePass, 20*time.Second)
	}
	return &Server{
		cfg: cfg, pool: pool, redis: rdb, media: ms, sessions: sm, customerSessions: cs, tmpl: tmpl,
		accounts: accounts.NewService(pool),
		auth:     auth.NewService(pool),
		catalog:  catalog.NewService(pool),
		orders:   ord,
		settings: st,
		reports:  reports.NewService(pool),
		offers:   offers.NewService(pool),
		courier:  courier.New(pool, ord, sf, cfg.SteadfastAutoSend),
		payments: payment.New(pool, ord, gw),
		cache:    cache.New(rdb, cfg.CacheTTL),
	}, nil
}

// RunBackground starts background jobs (courier status sync) until ctx is cancelled.
func (s *Server) RunBackground(ctx context.Context) {
	go s.courier.RunSync(ctx, s.cfg.SteadfastSyncInterval)
	if s.payments.Configured() {
		go s.payments.RunExpiry(ctx, 5*time.Minute, s.cfg.PaymentTimeout)
	}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	if s.cfg.TrustProxy {
		// One trusted hop (Caddy) appends the real peer to X-Forwarded-For, so we read the
		// rightmost entry. The app port must only be reachable through that proxy.
		r.Use(middleware.ClientIPFromXFFTrustedProxies(1))
	}
	r.Use(requestLogger)
	r.Use(s.recoverer)
	r.Use(securityHeaders(s.payments.GatewayOrigin()))
	r.Use(middleware.Compress(5, "text/html", "text/css", "application/javascript", "application/json"))
	r.NotFound(s.notFound)

	static, _ := fs.Sub(web.FS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", cacheControl(http.FileServer(http.FS(static)))))
	r.Get("/media/*", s.serveMedia)
	r.Get("/healthz", s.healthz)
	r.Get("/robots.txt", s.robots)
	r.Get("/sitemap.xml", s.h(s.sitemap))

	r.Route("/api/v1", s.apiRoutes)

	// SSLCommerz posts back cross-site (no CSRF token); every callback is verified with the
	// gateway's validation API before anything changes.
	r.Route("/payment/sslcommerz", func(r chi.Router) {
		r.Use(s.rateLimit("payment-callback", 60, time.Minute))
		r.Post("/return/{outcome}", s.h(s.paymentReturn))
		r.Post("/ipn", s.paymentIPN)
	})
	r.Get("/api/docs", s.h(s.apiDocs))
	r.Get("/api/openapi.yaml", s.openAPISpec)

	// HTML routes are CSRF-protected.
	r.Group(func(r chi.Router) {
		r.Use(s.csrf)
		r.Route("/admin", s.adminRoutes)
	})

	// Storefront: CSRF-protected, with the (optional) customer login loaded.
	r.Group(func(r chi.Router) {
		r.Use(s.csrf)
		r.Use(s.customerSessions.LoadAndSave)
		r.Use(s.loadCustomer)

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
		r.With(s.rateLimit("pay", 10, time.Minute)).Post("/order/{number}/pay", s.h(s.orderPay))
		r.Get("/track", s.h(s.trackPage))
		r.With(s.rateLimit("track", 15, time.Minute)).Post("/track", s.h(s.trackSubmit))
		r.Get("/policies", s.h(s.policies))

		r.Route("/account", s.accountRoutes)
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
