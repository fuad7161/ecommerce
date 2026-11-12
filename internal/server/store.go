package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/media"
	"github.com/fuad71/ecommerce/internal/money"
	"github.com/fuad71/ecommerce/internal/offers"
	"github.com/fuad71/ecommerce/internal/orders"
)

func parseListParams(r *http.Request) catalog.ListParams {
	q := r.URL.Query()
	lp := catalog.ListParams{
		Q:            strings.TrimSpace(q.Get("q")),
		CategorySlug: q.Get("category"),
		Sort:         q.Get("sort"),
		InStock:      q.Get("in_stock") == "1",
		OnSale:       q.Get("on_sale") == "1",
		PerPage:      12,
	}
	lp.Page, _ = strconv.Atoi(q.Get("page"))
	if v, err := money.Parse(q.Get("min")); err == nil {
		lp.MinPrice = v
	}
	if v, err := money.Parse(q.Get("max")); err == nil {
		lp.MaxPrice = v
	}
	return lp
}

func (s *Server) productList(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	lp := parseListParams(r)
	data := map[string]any{"Query": r.URL.Query(), "Params": lp, "Title": "All products", "BaseURL": "/products"}

	if sl := chi.URLParam(r, "offer"); sl != "" {
		o, err := s.offers.BySlug(ctx, sl)
		if errors.Is(err, offers.ErrNotFound) {
			return catalog.ErrNotFound
		}
		if err != nil {
			return err
		}
		lp.OfferID = o.ID
		data["Offer"] = o
		data["Title"] = o.Title
		data["Description"] = o.Description
		data["BaseURL"] = "/offers/" + o.Slug
	} else if sl := chi.URLParam(r, "slug"); sl != "" {
		cat, err := s.catalog.CategoryBySlug(ctx, sl)
		if err != nil {
			return err
		}
		lp.CategorySlug = cat.Slug
		data["Category"] = cat
		data["Title"] = cat.Name
		data["Description"] = cat.Description
		data["BaseURL"] = "/category/" + cat.Slug
	} else if lp.Q != "" {
		data["Title"] = fmt.Sprintf("Search results for “%s”", lp.Q)
	}

	page, err := s.catalog.ListProducts(ctx, lp)
	if err != nil {
		return err
	}
	data["Page"] = page
	data["Params"] = lp

	if isHTMX(r) && r.Header.Get("HX-Target") == "results" {
		s.render(w, r, http.StatusOK, "store/products", "results", data)
		return nil
	}
	cats, err := s.catalog.CategoryTree(ctx)
	if err != nil {
		return err
	}
	data["Categories"] = cats
	s.page(w, r, "store/products", data)
	return nil
}

func (s *Server) productDetail(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	p, err := s.catalog.ProductBySlug(ctx, chi.URLParam(r, "slug"), true)
	if err != nil {
		return err
	}
	related, err := s.catalog.Related(ctx, p, 4)
	if err != nil {
		return err
	}
	desc := p.Description
	if len([]rune(desc)) > 160 {
		desc = string([]rune(desc)[:157]) + "…"
	}
	data := map[string]any{"Product": p, "Related": related, "Title": p.Name, "Description": desc}
	if len(p.Images) > 0 {
		data["OGImage"] = s.cfg.AppURL + "/media/" + p.Images[0].Key
	}
	s.page(w, r, "store/product", data)
	return nil
}

func (s *Server) cartPage(w http.ResponseWriter, r *http.Request) error {
	s.page(w, r, "store/cart", map[string]any{"Title": "Your cart"})
	return nil
}

func parseCart(raw string) []orders.CartLine {
	var items []orders.CartLine
	if len(raw) > 20000 {
		return nil
	}
	_ = json.Unmarshal([]byte(raw), &items)
	return items
}

// cartSummary prices the browser's cart on the server and returns the summary fragment.
// It also tells the browser (via HX-Trigger) to store the corrected cart.
func (s *Server) cartSummary(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	items := parseCart(r.PostForm.Get("cart"))
	zoneID, _ := strconv.ParseInt(r.PostForm.Get("zone"), 10, 64)
	quote, err := s.orders.Quote(r.Context(), s.pool, items, zoneID)
	if errors.Is(err, orders.ErrInvalidZone) {
		quote, err = s.orders.Quote(r.Context(), s.pool, items, 0)
	}
	if err != nil {
		return err
	}
	trigger, _ := json.Marshal(map[string]any{"cart:normalize": map[string]any{"items": quote.Normalized()}})
	w.Header().Set("HX-Trigger", string(trigger))
	s.render(w, r, http.StatusOK, "store/cart", "cart_summary", map[string]any{
		"Quote": quote, "Mode": r.PostForm.Get("mode"), "ZoneChosen": zoneID > 0,
	})
	return nil
}

func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) checkoutPage(w http.ResponseWriter, r *http.Request) error {
	form := map[string]string{}
	if c := customerFrom(r); c != nil {
		// Pre-fill from the account's last delivery details. The customer can still change them.
		form = map[string]string{"name": c.Name, "phone": c.Phone, "district": c.District, "area": c.Area, "address": c.Address}
	}
	return s.renderCheckout(w, r, http.StatusOK, map[string]any{
		"IdempotencyKey": newIdempotencyKey(),
		"Form":           form,
		"Errors":         orders.FieldErrors{},
	})
}

func (s *Server) renderCheckout(w http.ResponseWriter, r *http.Request, status int, data map[string]any) error {
	zones, err := s.settings.Zones(r.Context(), true)
	if err != nil {
		return err
	}
	data["Zones"] = zones
	data["Title"] = "Checkout"
	data["OnlineAvailable"] = s.onlineAvailable(r.Context())
	if form, ok := data["Form"].(map[string]string); ok && form["payment_method"] == "" {
		form["payment_method"] = orders.PaymentCOD
		if data["OnlineAvailable"] == true {
			form["payment_method"] = orders.PaymentOnline
		}
	}
	s.render(w, r, status, "store/checkout", "layout", data)
	return nil
}

func (s *Server) checkoutSubmit(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	f := r.PostForm
	zoneID, _ := strconv.ParseInt(f.Get("zone"), 10, 64)
	in := orders.CheckoutInput{
		Name: f.Get("name"), Phone: f.Get("phone"), District: f.Get("district"), Area: f.Get("area"),
		Address: f.Get("address"), Notes: f.Get("notes"), ZoneID: zoneID,
		PaymentMethod: f.Get("payment_method"), Confirmed: f.Get("confirm") == "1",
		IdempotencyKey: f.Get("idempotency_key"), Items: parseCart(f.Get("cart")),
		OnlineAvailable: s.onlineAvailable(r.Context()),
	}
	if c := customerFrom(r); c != nil {
		in.AccountID = c.ID
	}
	order, err := s.orders.PlaceOrder(r.Context(), in)
	if err != nil {
		form := map[string]string{}
		for k := range f {
			form[k] = f.Get(k)
		}
		data := map[string]any{"Form": form, "IdempotencyKey": in.IdempotencyKey, "Errors": orders.FieldErrors{}}
		var fe orders.FieldErrors
		switch {
		case errors.As(err, &fe):
			data["Errors"] = fe
		case errors.Is(err, orders.ErrCartChanged), errors.Is(err, orders.ErrEmptyCart),
			errors.Is(err, orders.ErrInvalidZone), errors.Is(err, orders.ErrCODDisabled):
			data["Error"] = err.Error()
		default:
			return err
		}
		if data["Error"] == nil {
			data["Error"] = "Please fix the highlighted fields."
		}
		return s.renderCheckout(w, r, http.StatusUnprocessableEntity, data)
	}
	slog.InfoContext(r.Context(), "order placed", "order", order.Number, "total", order.Total, "payment", order.PaymentMethod)
	if order.AwaitingPayment() {
		s.redirectToGateway(w, r, order)
		return nil
	}
	http.Redirect(w, r, s.orderURL(order.Number, ""), http.StatusSeeOther)
	return nil
}

// orderToken lets the customer who placed an order view its confirmation page without logging in.
func (s *Server) orderToken(number string) string {
	m := hmac.New(sha256.New, []byte(s.cfg.SecretKey))
	m.Write([]byte("order-confirmation:" + number))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

func (s *Server) validOrderToken(number, token string) bool {
	return number != "" && hmac.Equal([]byte(token), []byte(s.orderToken(number)))
}

func (s *Server) orderConfirmation(w http.ResponseWriter, r *http.Request) error {
	number := chi.URLParam(r, "number")
	if !s.validOrderToken(number, r.URL.Query().Get("t")) {
		return orders.ErrNotFound
	}
	o, err := s.orders.OrderByNumber(r.Context(), number)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	data := map[string]any{"Order": o, "Title": "Order " + o.Number,
		"Token": s.orderToken(o.Number), "Pay": r.URL.Query().Get("pay"), "OnlineAvailable": s.onlineAvailable(r.Context())}
	if c := customerFrom(r); c != nil && o.AccountID != nil && *o.AccountID == c.ID {
		data["InAccount"] = true
	} else if o.AccountID == nil {
		// Guest order: offer to save it to an account (the signed link proves ownership).
		data["CanSave"] = true
	}
	s.page(w, r, "store/order", data)
	return nil
}

func (s *Server) trackPage(w http.ResponseWriter, r *http.Request) error {
	s.page(w, r, "store/track", map[string]any{"Title": "Track your order", "Number": r.URL.Query().Get("order")})
	return nil
}

func (s *Server) trackSubmit(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	number, ph := r.PostForm.Get("order"), r.PostForm.Get("phone")
	data := map[string]any{"Title": "Track your order", "Number": number, "Phone": ph, "Searched": true}
	o, err := s.orders.Track(r.Context(), number, ph)
	switch {
	case err == nil:
		data["Order"] = o
	case errors.Is(err, orders.ErrNotFound):
		// Same message whether the order or the phone is wrong, so numbers can't be probed.
	default:
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	if isHTMX(r) {
		s.render(w, r, http.StatusOK, "store/track", "track_result", data)
		return nil
	}
	s.page(w, r, "store/track", data)
	return nil
}

func (s *Server) policies(w http.ResponseWriter, r *http.Request) error {
	s.page(w, r, "store/policies", map[string]any{"Title": "Store policies"})
	return nil
}

func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "*")
	if key == "" || strings.Contains(key, "..") {
		http.NotFound(w, r)
		return
	}
	obj, err := s.media.Get(r.Context(), key)
	if err != nil && media.IsNotFound(err) {
		// Images uploaded before thumbnails existed: serve the original instead.
		if main, ok := media.MainKey(key); ok {
			obj, err = s.media.Get(r.Context(), main)
		}
	}
	if err != nil {
		if !media.IsNotFound(err) {
			slog.ErrorContext(r.Context(), "media fetch", "key", key, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	defer obj.Close()
	etag := `"` + obj.ETag + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", obj.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", etag)
	_, _ = io.Copy(w, obj)
}

func (s *Server) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "User-agent: *\nDisallow: /admin\nDisallow: /cart\nDisallow: /checkout\nDisallow: /order/\nDisallow: /api/\n\nSitemap: %s/sitemap.xml\n", s.cfg.AppURL)
}

func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) error {
	type loc struct {
		Loc string `xml:"loc"`
	}
	type urlset struct {
		XMLName xml.Name `xml:"urlset"`
		NS      string   `xml:"xmlns,attr"`
		URLs    []loc    `xml:"url"`
	}
	set := urlset{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	set.URLs = append(set.URLs, loc{s.cfg.AppURL + "/"}, loc{s.cfg.AppURL + "/products"})
	cats, err := s.catalog.Categories(r.Context(), true)
	if err != nil {
		return err
	}
	for _, c := range cats {
		set.URLs = append(set.URLs, loc{s.cfg.AppURL + "/category/" + c.Slug})
	}
	for page := 1; ; page++ {
		p, err := s.catalog.ListProducts(r.Context(), catalog.ListParams{Page: page, PerPage: 100})
		if err != nil {
			return err
		}
		for _, it := range p.Items {
			set.URLs = append(set.URLs, loc{s.cfg.AppURL + "/product/" + it.Slug})
		}
		if !p.HasNext() || page >= 500 {
			break
		}
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = io.WriteString(w, xml.Header)
	return xml.NewEncoder(w).Encode(set)
}
