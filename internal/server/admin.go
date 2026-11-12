package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/fuad71/ecommerce/internal/audit"
	"github.com/fuad71/ecommerce/internal/auth"
	"github.com/fuad71/ecommerce/internal/money"
	"github.com/fuad71/ecommerce/internal/orders"
	"github.com/fuad71/ecommerce/internal/settings"
)

func (s *Server) adminRoutes(r chi.Router) {
	r.Use(s.sessions.LoadAndSave)
	r.Use(s.loadAdmin)

	r.Get("/login", s.h(s.loginPage))
	r.With(s.rateLimit("admin-login", 10, 5*time.Minute)).Post("/login", s.h(s.loginSubmit))

	r.Group(func(r chi.Router) {
		r.Use(s.requireAdmin)
		r.Use(s.bumpCacheOnWrite)
		r.Post("/logout", s.h(s.logout))
		r.Get("/account", s.h(s.adminAccount))
		r.Post("/account/password", s.h(s.adminAccountPassword))

		r.With(s.requirePerm(auth.PermViewDashboard)).Get("/", s.h(s.dashboard))

		r.Group(func(r chi.Router) {
			r.Use(s.requirePerm(auth.PermManageOrders))
			r.Get("/orders", s.h(s.adminOrders))
			r.Get("/orders/export.csv", s.h(s.adminOrdersExport))
			r.Get("/orders/{id}", s.h(s.adminOrder))
			r.Get("/orders/{id}/invoice", s.h(s.adminInvoice))
			r.Post("/orders/{id}/status", s.h(s.adminOrderStatus))
			r.Post("/orders/{id}/notes", s.h(s.adminOrderNote))
		})

		r.Group(func(r chi.Router) {
			r.Use(s.requirePerm(auth.PermManageCourier))
			r.Post("/orders/{id}/courier", s.h(s.adminCourierSend))
			r.Post("/orders/{id}/courier/sync", s.h(s.adminCourierSync))
			r.Get("/orders/{id}/courier/eligibility", s.h(s.adminCourierEligibility))
		})

		r.Group(func(r chi.Router) {
			r.Use(s.requirePerm(auth.PermManageOffers))
			r.Get("/offers", s.h(s.adminOffers))
			r.Get("/offers/new", s.h(s.adminOfferForm))
			r.Post("/offers", s.h(s.adminOfferSave))
			r.Get("/offers/{id}", s.h(s.adminOfferForm))
			r.Post("/offers/{id}", s.h(s.adminOfferSave))
			r.Post("/offers/{id}/delete", s.h(s.adminOfferDelete))
		})

		r.Group(func(r chi.Router) {
			r.Use(s.requirePerm(auth.PermViewCustomers))
			r.Get("/customers", s.h(s.adminCustomers))
			r.Get("/customers/{id}", s.h(s.adminCustomer))
			r.Post("/customers/{id}/notes", s.h(s.adminCustomerNotes))
		})

		r.Group(func(r chi.Router) {
			r.Use(s.requirePerm(auth.PermManageCatalog))
			r.Get("/products", s.h(s.adminProducts))
			r.Get("/products/new", s.h(s.adminProductNew))
			r.Post("/products", s.h(s.adminProductSave))
			r.Get("/products/{id}", s.h(s.adminProductEdit))
			r.Get("/products/{id}/preview", s.h(s.adminProductPreview))
			r.Post("/products/{id}", s.h(s.adminProductSave))
			r.Post("/products/{id}/delete", s.h(s.adminProductDelete))
			r.Post("/products/{id}/images", s.h(s.adminProductImages))
			r.Post("/products/{id}/images/{imageID}/delete", s.h(s.adminProductImageDelete))
			r.Post("/products/{id}/images/{imageID}/primary", s.h(s.adminProductImagePrimary))
			r.Post("/products/{id}/variants", s.h(s.adminVariantAdd))
			r.Post("/variants/{id}", s.h(s.adminVariantUpdate))

			r.Get("/categories", s.h(s.adminCategories))
			r.Get("/categories/new", s.h(s.adminCategoryForm))
			r.Post("/categories", s.h(s.adminCategorySave))
			r.Get("/categories/{id}", s.h(s.adminCategoryForm))
			r.Post("/categories/{id}", s.h(s.adminCategorySave))
			r.Post("/categories/{id}/delete", s.h(s.adminCategoryDelete))
		})

		r.Group(func(r chi.Router) {
			r.Use(s.requirePerm(auth.PermManageStock))
			r.Post("/variants/{id}/adjust", s.h(s.adminVariantAdjust))
			r.Get("/variants/{id}/history", s.h(s.adminVariantHistory))
		})

		r.Group(func(r chi.Router) {
			r.Use(s.requirePerm(auth.PermManageSetting))
			r.Get("/settings", s.h(s.adminSettings))
			r.Post("/settings", s.h(s.adminSettingsSave))
			r.Post("/settings/zones", s.h(s.adminZoneSave))
			r.Get("/homepage", s.h(s.adminHomepage))
			r.Post("/homepage", s.h(s.adminHomepageSave))
			r.Get("/courier/balance", s.h(s.adminCourierBalance))
			r.Get("/admins", s.h(s.adminAdmins))
			r.Post("/admins", s.h(s.adminAdminCreate))
			r.Post("/admins/{id}", s.h(s.adminAdminUpdate))
		})

		r.With(s.requirePerm(auth.PermViewAudit)).Get("/audit", s.h(s.adminAudit))
	})
}

// ---------- helpers ----------

func (s *Server) logErr(r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "request failed", "err", err, "path", r.URL.Path,
		"request_id", middleware.GetReqID(r.Context()))
}

func idParam(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return id
}

func formInt(r *http.Request, key string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(key)))
	return n
}

func formInt64(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue(key)), 10, 64)
	return n
}

func (s *Server) audit(r *http.Request, action, entity string, entityID any, details map[string]any) {
	a := adminFrom(r)
	var id int64
	if a != nil {
		id = a.ID
	}
	audit.Log(r.Context(), s.pool, id, clientIP(r), action, entity, fmt.Sprint(entityID), details)
}

// redirect handles both normal and HTMX form posts.
func redirect(w http.ResponseWriter, r *http.Request, url string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// ---------- auth ----------

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) error {
	if adminFrom(r) != nil {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return nil
	}
	s.page(w, r, "admin/login", map[string]any{"Title": "Admin login"})
	return nil
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) error {
	email, password := r.FormValue("email"), r.FormValue("password")
	a, err := s.auth.Authenticate(r.Context(), email, password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		audit.Log(r.Context(), s.pool, 0, clientIP(r), "auth.login_failed", "admin", "", map[string]any{"email": email})
		s.render(w, r, http.StatusUnauthorized, "admin/login", "layout", map[string]any{
			"Title": "Admin login", "Error": err.Error(), "Email": email,
		})
		return nil
	}
	if err != nil {
		return err
	}
	// Prevent session fixation.
	if err := s.sessions.RenewToken(r.Context()); err != nil {
		return err
	}
	s.sessions.Put(r.Context(), "admin_id", a.ID)
	audit.Log(r.Context(), s.pool, a.ID, clientIP(r), "auth.login", "admin", strconv.FormatInt(a.ID, 10), nil)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	s.audit(r, "auth.logout", "admin", adminFrom(r).ID, nil)
	if err := s.sessions.Destroy(r.Context()); err != nil {
		return err
	}
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
	return nil
}

// ---------- dashboard ----------

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) error {
	ov, err := s.reports.Overview(r.Context())
	if err != nil {
		return err
	}
	s.page(w, r, "admin/dashboard", map[string]any{"Title": "Dashboard", "O": ov})
	return nil
}

// ---------- orders ----------

func parseOrderFilters(r *http.Request) orders.ListParams {
	q := r.URL.Query()
	lp := orders.ListParams{Q: q.Get("q"), Status: q.Get("status"), Payment: q.Get("payment")}
	lp.Page, _ = strconv.Atoi(q.Get("page"))
	if t, err := time.ParseInLocation("2006-01-02", q.Get("from"), orders.Dhaka()); err == nil {
		lp.From = t
	}
	if t, err := time.ParseInLocation("2006-01-02", q.Get("to"), orders.Dhaka()); err == nil {
		lp.To = t
	}
	return lp
}

func (s *Server) adminOrders(w http.ResponseWriter, r *http.Request) error {
	lp := parseOrderFilters(r)
	page, err := s.orders.List(r.Context(), lp)
	if err != nil {
		return err
	}
	data := map[string]any{"Title": "Orders", "Page": page, "F": lp, "Query": r.URL.Query()}
	if isHTMX(r) && r.Header.Get("HX-Target") == "results" {
		s.render(w, r, http.StatusOK, "admin/orders", "results", data)
		return nil
	}
	s.page(w, r, "admin/orders", data)
	return nil
}

func (s *Server) adminOrdersExport(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="orders-%s.csv"`, time.Now().Format("20060102-1504")))
	s.audit(r, "orders.export", "order", "", map[string]any{"query": r.URL.RawQuery})
	return s.orders.ExportCSV(r.Context(), w, parseOrderFilters(r))
}

func (s *Server) loadAdminOrder(r *http.Request) (*orders.Order, error) {
	o, err := s.orders.OrderByID(r.Context(), idParam(r, "id"))
	if err != nil {
		return nil, err
	}
	o.Notes, err = s.orders.Notes(r.Context(), o.ID)
	return o, err
}

func (s *Server) adminOrder(w http.ResponseWriter, r *http.Request) error {
	o, err := s.loadAdminOrder(r)
	if err != nil {
		return err
	}
	courierData, err := s.courierData(r, o)
	if err != nil {
		return err
	}
	payments, err := s.payments.ForOrder(r.Context(), o.ID)
	if err != nil {
		return err
	}
	s.page(w, r, "admin/order", map[string]any{"Title": "Order " + o.Number, "Order": o, "Courier": courierData,
		"CanCourier": adminFrom(r).Can(auth.PermManageCourier), "Payments": payments})
	return nil
}

func (s *Server) adminInvoice(w http.ResponseWriter, r *http.Request) error {
	o, err := s.orders.OrderByID(r.Context(), idParam(r, "id"))
	if err != nil {
		return err
	}
	s.render(w, r, http.StatusOK, "admin/invoice", "invoice", map[string]any{"Order": o})
	return nil
}

func (s *Server) adminOrderStatus(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	to, note := r.FormValue("status"), r.FormValue("note")
	err := s.orders.UpdateStatus(r.Context(), adminFrom(r).ID, id, to, note)
	switch {
	case errors.Is(err, orders.ErrInvalidTransition):
		s.flash(r, "That status change is not allowed.")
	case errors.Is(err, orders.ErrAwaitingPayment):
		s.flash(r, "This order is waiting for online payment. Confirm it after the payment arrives, or cancel it.")
	case err != nil:
		return err
	default:
		s.audit(r, "order.status", "order", id, map[string]any{"to": to, "note": note})
		msg := "Order status updated to " + orders.StatusLabel(to) + "."
		if to == orders.StatusConfirmed && s.courier.AutoSend {
			// STEADFAST_AUTO_SEND: confirming an order also books the courier.
			if c, err := s.courier.Send(r.Context(), adminFrom(r).ID, id, ""); err != nil {
				msg += " Automatic Steadfast booking failed: " + courierErrorMessage(err)
			} else {
				s.audit(r, "courier.create", "order", id, map[string]any{"consignment_id": c.ConsignmentID, "auto": true})
				msg += fmt.Sprintf(" Steadfast consignment #%d created.", *c.ConsignmentID)
			}
		}
		s.flash(r, msg)
	}
	redirect(w, r, fmt.Sprintf("/admin/orders/%d", id))
	return nil
}

func (s *Server) adminOrderNote(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	if err := s.orders.AddNote(r.Context(), adminFrom(r).ID, id, r.FormValue("note")); err != nil {
		var fe orders.FieldErrors
		if !errors.As(err, &fe) {
			return err
		}
		s.flash(r, "Note cannot be empty.")
	}
	redirect(w, r, fmt.Sprintf("/admin/orders/%d#notes", id))
	return nil
}

// ---------- customers ----------

func (s *Server) adminCustomers(w http.ResponseWriter, r *http.Request) error {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	p, err := s.orders.Customers(r.Context(), r.URL.Query().Get("q"), page)
	if err != nil {
		return err
	}
	data := map[string]any{"Title": "Customers", "Page": p, "Q": r.URL.Query().Get("q"), "Query": r.URL.Query()}
	if isHTMX(r) && r.Header.Get("HX-Target") == "results" {
		s.render(w, r, http.StatusOK, "admin/customers", "results", data)
		return nil
	}
	s.page(w, r, "admin/customers", data)
	return nil
}

func (s *Server) adminCustomer(w http.ResponseWriter, r *http.Request) error {
	c, list, err := s.orders.Customer(r.Context(), idParam(r, "id"))
	if err != nil {
		return err
	}
	s.page(w, r, "admin/customer", map[string]any{"Title": c.Name, "Customer": c, "Orders": list})
	return nil
}

func (s *Server) adminCustomerNotes(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	if err := s.orders.SaveCustomerNotes(r.Context(), id, r.FormValue("notes")); err != nil {
		return err
	}
	s.audit(r, "customer.notes", "customer", id, nil)
	s.flash(r, "Customer notes saved.")
	redirect(w, r, fmt.Sprintf("/admin/customers/%d", id))
	return nil
}

// ---------- settings ----------

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request) error {
	zones, err := s.settings.Zones(r.Context(), false)
	if err != nil {
		return err
	}
	keyHint := ""
	if k := s.cfg.SteadfastAPIKey; len(k) > 4 {
		keyHint = "••••" + k[len(k)-4:]
	}
	s.page(w, r, "admin/settings", map[string]any{"Title": "Store settings", "Zones": zones,
		"Steadfast": map[string]any{"Configured": s.courier.Configured(), "KeyHint": keyHint,
			"AutoSend": s.courier.AutoSend, "Interval": s.cfg.SteadfastSyncInterval.String()},
		"Payments": map[string]any{"Configured": s.payments.Configured(), "Sandbox": s.cfg.SSLCommerzSandbox,
			"Timeout": s.cfg.PaymentTimeout.String()}})
	return nil
}

func (s *Server) adminSettingsSave(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	values := map[string]string{}
	for _, k := range settings.Keys {
		values[k] = strings.TrimSpace(r.PostForm.Get(k))
	}
	values["cod_enabled"] = strconv.FormatBool(r.PostForm.Get("cod_enabled") == "1")
	values["online_payment_enabled"] = strconv.FormatBool(r.PostForm.Get("online_payment_enabled") == "1")
	threshold, err := money.Parse(values["free_delivery_threshold"])
	if values["free_delivery_threshold"] == "" {
		threshold, err = 0, nil
	}
	if err != nil {
		s.flash(r, "Free delivery threshold must be an amount in taka (0 to disable).")
		redirect(w, r, "/admin/settings")
		return nil
	}
	values["free_delivery_threshold"] = strconv.FormatInt(threshold, 10)
	if values["store_name"] == "" {
		values["store_name"] = "My Store"
	}
	if err := s.settings.Save(r.Context(), values); err != nil {
		return err
	}
	s.audit(r, "settings.update", "settings", "", nil)
	s.flash(r, "Settings saved.")
	redirect(w, r, "/admin/settings")
	return nil
}

func (s *Server) adminZoneSave(w http.ResponseWriter, r *http.Request) error {
	charge, err := money.Parse(r.FormValue("charge"))
	name := strings.TrimSpace(r.FormValue("name"))
	if err != nil || name == "" {
		s.flash(r, "Zone name and a valid charge are required.")
		redirect(w, r, "/admin/settings#zones")
		return nil
	}
	z := settings.Zone{ID: formInt64(r, "id"), Name: name, Charge: charge, SortOrder: formInt(r, "sort_order"),
		Active: r.FormValue("active") == "1"}
	if err := s.settings.SaveZone(r.Context(), z); err != nil {
		return err
	}
	s.audit(r, "settings.zone", "delivery_zone", z.ID, map[string]any{"name": z.Name, "charge": z.Charge, "active": z.Active})
	s.flash(r, "Delivery zone saved.")
	redirect(w, r, "/admin/settings#zones")
	return nil
}

// ---------- admins & audit ----------

func (s *Server) adminAdmins(w http.ResponseWriter, r *http.Request) error {
	list, err := s.auth.List(r.Context())
	if err != nil {
		return err
	}
	s.page(w, r, "admin/admins", map[string]any{"Title": "Admin users", "Admins": list})
	return nil
}

func (s *Server) adminAdminCreate(w http.ResponseWriter, r *http.Request) error {
	a, err := s.auth.Create(r.Context(), r.FormValue("email"), r.FormValue("name"), r.FormValue("password"), r.FormValue("role"))
	if err != nil {
		s.flash(r, "Could not create user: "+err.Error())
	} else {
		s.audit(r, "admin.create", "admin", a.ID, map[string]any{"email": a.Email, "role": a.Role})
		s.flash(r, "User "+a.Email+" created.")
	}
	redirect(w, r, "/admin/admins")
	return nil
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) error {
	page := max(formInt(r, "page"), 1)
	entries, err := audit.Recent(r.Context(), s.pool, 50, (page-1)*50)
	if err != nil {
		return err
	}
	s.page(w, r, "admin/audit", map[string]any{"Title": "Audit log", "Entries": entries, "PageNum": page,
		"HasNext": len(entries) == 50})
	return nil
}
