package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/orders"
)

// Public JSON API (v1). It does not use cookies, so it is not CSRF-protected;
// all prices and totals are computed server-side.

type apiError struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) apiRoutes(r chi.Router) {
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/categories", s.api(s.apiCategories))
	r.Get("/products", s.api(s.apiProducts))
	r.Get("/products/{slug}", s.api(s.apiProduct))
	r.Get("/delivery-zones", s.api(s.apiZones))
	r.Post("/cart/quote", s.api(s.apiQuote))
	r.With(s.rateLimit("api-checkout", 10, time.Minute)).Post("/orders", s.api(s.apiPlaceOrder))
	r.With(s.rateLimit("api-track", 15, time.Minute)).Post("/orders/track", s.api(s.apiTrack))
}

func (s *Server) api(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err == nil {
			return
		}
		var fe orders.FieldErrors
		switch {
		case errors.As(err, &fe):
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "validation failed", Fields: fe})
		case errors.Is(err, catalog.ErrNotFound), errors.Is(err, orders.ErrNotFound):
			writeJSON(w, http.StatusNotFound, apiError{Error: "not found"})
		case errors.Is(err, orders.ErrCartChanged), errors.Is(err, orders.ErrEmptyCart),
			errors.Is(err, orders.ErrInvalidZone), errors.Is(err, orders.ErrCODDisabled):
			writeJSON(w, http.StatusConflict, apiError{Error: err.Error()})
		case errors.Is(err, errBadJSON):
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		default:
			s.logErr(r, err)
			writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal error"})
		}
	}
}

var errBadJSON = errors.New("invalid JSON body")

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errBadJSON
	}
	return nil
}

func (s *Server) apiCategories(w http.ResponseWriter, r *http.Request) error {
	cats, err := s.catalog.CategoryTree(r.Context())
	if err != nil {
		return err
	}
	type cat struct {
		Name     string `json:"name"`
		Slug     string `json:"slug"`
		Children []cat  `json:"children,omitempty"`
	}
	out := make([]cat, 0, len(cats))
	for _, c := range cats {
		cc := cat{Name: c.Name, Slug: c.Slug}
		for _, ch := range c.Children {
			cc.Children = append(cc.Children, cat{Name: ch.Name, Slug: ch.Slug})
		}
		out = append(out, cc)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
	return nil
}

type apiProductCard struct {
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	Category     string `json:"category"`
	RegularPrice int64  `json:"regular_price"`
	Price        int64  `json:"price"`
	Discount     int    `json:"discount_percent"`
	Offer        string `json:"offer,omitempty"`
	InStock      bool   `json:"in_stock"`
	Image        string `json:"image,omitempty"`
}

func (s *Server) apiProducts(w http.ResponseWriter, r *http.Request) error {
	lp := parseListParams(r)
	page, err := s.catalog.ListProducts(r.Context(), lp)
	if err != nil {
		return err
	}
	items := make([]apiProductCard, 0, len(page.Items))
	for _, p := range page.Items {
		c := apiProductCard{Name: p.Name, Slug: p.Slug, Category: p.CategoryName, RegularPrice: p.RegularPrice,
			Price: p.Price, Discount: p.DiscountPercent(), Offer: p.OfferTitle, InStock: p.Available > 0}
		if p.ImageKey != "" {
			c.Image = "/media/" + p.ImageKey
		}
		items = append(items, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": items,
		"meta": map[string]int{"page": page.Page, "per_page": page.PerPage, "total": page.Total, "pages": page.Pages()},
	})
	return nil
}

func (s *Server) apiProduct(w http.ResponseWriter, r *http.Request) error {
	p, err := s.catalog.ProductBySlug(r.Context(), chi.URLParam(r, "slug"), true)
	if err != nil {
		return err
	}
	type variant struct {
		SKU       string `json:"sku"`
		Name      string `json:"name"`
		Available int    `json:"available"`
	}
	vs := []variant{}
	for _, v := range p.ActiveVariants() {
		vs = append(vs, variant{SKU: v.SKU, Name: v.Name, Available: v.Available()})
	}
	images := []string{}
	for _, i := range p.Images {
		images = append(images, "/media/"+i.Key)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"name": p.Name, "slug": p.Slug, "sku": p.SKU, "brand": p.Brand, "description": p.Description,
		"specs": p.Specs, "category": p.CategoryName, "regular_price": p.RegularPrice, "price": p.Price, "offer": p.OfferTitle,
		"discount_percent": p.DiscountPercent(), "images": images, "variants": vs,
	}})
	return nil
}

func (s *Server) apiZones(w http.ResponseWriter, r *http.Request) error {
	zones, err := s.settings.Zones(r.Context(), true)
	if err != nil {
		return err
	}
	type zone struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Charge int64  `json:"charge"`
	}
	out := []zone{}
	for _, z := range zones {
		out = append(out, zone{z.ID, z.Name, z.Charge})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
	return nil
}

func (s *Server) apiQuote(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Items  []orders.CartLine `json:"items"`
		ZoneID int64             `json:"zone_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	q, err := s.orders.Quote(r.Context(), s.pool, body.Items, body.ZoneID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": q})
	return nil
}

type apiOrder struct {
	OrderNumber    string     `json:"order_number"`
	Status         string     `json:"status"`
	StatusLabel    string     `json:"status_label"`
	CustomerName   string     `json:"customer_name"`
	Address        string     `json:"delivery_address"`
	PaymentMethod  string     `json:"payment_method"`
	Subtotal       int64      `json:"subtotal"`
	Discount       int64      `json:"discount"`
	DeliveryCharge int64      `json:"delivery_charge"`
	Total          int64      `json:"total"`
	Items          []apiItem  `json:"items"`
	History        []apiEvent `json:"history,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type apiItem struct {
	Name      string `json:"name"`
	Variant   string `json:"variant,omitempty"`
	Quantity  int    `json:"quantity"`
	UnitPrice int64  `json:"unit_price"`
	LineTotal int64  `json:"line_total"`
}

type apiEvent struct {
	Status string    `json:"status"`
	At     time.Time `json:"at"`
}

func toAPIOrder(o *orders.Order, withHistory bool) apiOrder {
	out := apiOrder{
		OrderNumber: o.Number, Status: o.Status, StatusLabel: o.StatusLabel(), CustomerName: o.CustomerName,
		Address:       o.Address + ", " + o.Area + ", " + o.District,
		PaymentMethod: o.PaymentMethod, Subtotal: o.Subtotal, Discount: o.Discount,
		DeliveryCharge: o.DeliveryCharge, Total: o.Total, CreatedAt: o.CreatedAt,
	}
	for _, it := range o.Items {
		out.Items = append(out.Items, apiItem{it.ProductName, it.VariantName, it.Quantity, it.UnitPrice, it.LineTotal})
	}
	if withHistory {
		for _, h := range o.History {
			out.History = append(out.History, apiEvent{h.To, h.CreatedAt})
		}
	}
	return out
}

func (s *Server) apiPlaceOrder(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Name          string            `json:"name"`
		Phone         string            `json:"phone"`
		District      string            `json:"district"`
		Area          string            `json:"area"`
		Address       string            `json:"address"`
		Notes         string            `json:"notes"`
		ZoneID        int64             `json:"zone_id"`
		PaymentMethod string            `json:"payment_method"`
		Confirmed     bool              `json:"confirmed"`
		Items         []orders.CartLine `json:"items"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	if body.PaymentMethod == orders.PaymentOnline {
		// The hosted gateway needs a browser redirect; API clients use cash on delivery.
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "online payment is only available on the website; use payment_method \"cod\""})
		return nil
	}
	o, err := s.orders.PlaceOrder(r.Context(), orders.CheckoutInput{
		Name: body.Name, Phone: body.Phone, District: body.District, Area: body.Area, Address: body.Address,
		Notes: body.Notes, ZoneID: body.ZoneID, PaymentMethod: body.PaymentMethod, Confirmed: body.Confirmed,
		IdempotencyKey: r.Header.Get("Idempotency-Key"), Items: body.Items,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": toAPIOrder(o, false)})
	return nil
}

func (s *Server) apiTrack(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		OrderNumber string `json:"order_number"`
		Phone       string `json:"phone"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	o, err := s.orders.Track(r.Context(), body.OrderNumber, body.Phone)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": toAPIOrder(o, true)})
	return nil
}
