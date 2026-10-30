package server

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fuad71/ecommerce/internal/auth"
	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/courier/steadfast"
	"github.com/fuad71/ecommerce/internal/media"
	"github.com/fuad71/ecommerce/internal/money"
	"github.com/fuad71/ecommerce/internal/offers"
	"github.com/fuad71/ecommerce/internal/orders"
	"github.com/fuad71/ecommerce/web"
)

// bumpCacheOnWrite invalidates cached catalog/homepage data after any successful admin POST.
func (s *Server) bumpCacheOnWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.Method == http.MethodPost {
			s.cache.Bump(r.Context())
		}
	})
}

// ---------- offers ----------

func (s *Server) adminOffers(w http.ResponseWriter, r *http.Request) error {
	list, err := s.offers.List(r.Context())
	if err != nil {
		return err
	}
	s.page(w, r, "admin/offers", map[string]any{"Title": "Offers & campaigns", "Offers": list, "Now": time.Now()})
	return nil
}

func (s *Server) adminOfferForm(w http.ResponseWriter, r *http.Request) error {
	o := &offers.Offer{Active: true, ShowOnHome: true, DiscountType: offers.TypePercent,
		StartsAt: time.Now().Truncate(time.Hour), EndsAt: time.Now().Truncate(time.Hour).AddDate(0, 0, 7)}
	if id := idParam(r, "id"); id > 0 {
		var err error
		if o, err = s.offers.Get(r.Context(), id); err != nil {
			if errors.Is(err, offers.ErrNotFound) {
				return catalog.ErrNotFound
			}
			return err
		}
	}
	return s.renderOfferForm(w, r, http.StatusOK, o, nil)
}

func (s *Server) renderOfferForm(w http.ResponseWriter, r *http.Request, status int, o *offers.Offer, errs map[string]string) error {
	products, err := s.catalog.Briefs(r.Context())
	if err != nil {
		return err
	}
	cats, err := s.catalog.Categories(r.Context(), false)
	if err != nil {
		return err
	}
	if errs == nil {
		errs = map[string]string{}
	}
	title := "New campaign"
	if o.ID > 0 {
		title = o.Title
	}
	value := strconv.FormatInt(o.DiscountValue, 10)
	if o.DiscountType == offers.TypeFixed {
		value = money.Plain(o.DiscountValue)
	}
	if o.DiscountValue == 0 {
		value = ""
	}
	s.render(w, r, status, "admin/offer_form", "layout", map[string]any{
		"Title": title, "Offer": o, "Products": products, "Categories": cats, "Errors": errs, "Value": value,
		"Starts": o.StartsAt.In(orders.Dhaka()).Format("2006-01-02T15:04"),
		"Ends":   o.EndsAt.In(orders.Dhaka()).Format("2006-01-02T15:04"),
	})
	return nil
}

func parseIDs(vals []string) []int64 {
	var out []int64
	for _, v := range vals {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func (s *Server) adminOfferSave(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return err
	}
	o := &offers.Offer{ID: idParam(r, "id")}
	if o.ID > 0 {
		existing, err := s.offers.Get(r.Context(), o.ID)
		if err != nil {
			return catalog.ErrNotFound
		}
		o.BannerKey = existing.BannerKey
	}
	f := r.MultipartForm.Value
	get := func(k string) string {
		if v := f[k]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}
	o.Title, o.Slug, o.Description = get("title"), get("slug"), get("description")
	o.DiscountType = get("discount_type")
	o.Active, o.ShowOnHome = get("active") == "1", get("show_on_home") == "1"
	o.SortOrder, _ = strconv.Atoi(get("sort_order"))
	o.ProductIDs, o.CategoryIDs = parseIDs(f["product_ids"]), parseIDs(f["category_ids"])

	errs := map[string]string{}
	if o.DiscountType == offers.TypeFixed {
		v, err := money.Parse(get("discount_value"))
		if err != nil {
			errs["discount_value"] = "Enter an amount in taka"
		}
		o.DiscountValue = v
	} else {
		o.DiscountValue, _ = strconv.ParseInt(get("discount_value"), 10, 64)
	}
	var err error
	if o.StartsAt, err = time.ParseInLocation("2006-01-02T15:04", get("starts_at"), orders.Dhaka()); err != nil {
		errs["dates"] = "Enter a valid start date"
	}
	if o.EndsAt, err = time.ParseInLocation("2006-01-02T15:04", get("ends_at"), orders.Dhaka()); err != nil {
		errs["dates"] = "Enter a valid end date"
	}
	if file, fh, ferr := r.FormFile("banner"); ferr == nil {
		key, uerr := s.media.Upload(r.Context(), "offers", file)
		file.Close()
		if media.IsUserError(uerr) {
			errs["banner"] = fh.Filename + ": " + uerr.Error()
		} else if uerr != nil {
			return uerr
		} else {
			o.BannerKey = key
		}
	}
	if len(errs) == 0 {
		err := s.offers.Save(r.Context(), o)
		var verr offers.ValidationError
		switch {
		case errors.As(err, &verr):
			errs = verr
		case err != nil:
			return err
		}
	}
	if len(errs) > 0 {
		return s.renderOfferForm(w, r, http.StatusUnprocessableEntity, o, errs)
	}
	s.audit(r, "offer.save", "offer", o.ID, map[string]any{"title": o.Title, "type": o.DiscountType,
		"value": o.DiscountValue, "active": o.Active, "products": len(o.ProductIDs), "categories": len(o.CategoryIDs)})
	s.flash(r, "Campaign saved.")
	redirect(w, r, "/admin/offers")
	return nil
}

func (s *Server) adminOfferDelete(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	if err := s.offers.Delete(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "offer.delete", "offer", id, nil)
	s.flash(r, "Campaign deleted.")
	redirect(w, r, "/admin/offers")
	return nil
}

// ---------- homepage sections ----------

func (s *Server) adminHomepage(w http.ResponseWriter, r *http.Request) error {
	st, err := s.settings.All(r.Context())
	if err != nil {
		return err
	}
	enabled := enabledSections(st.Get("home_sections"))
	type row struct {
		Key, Title string
		Enabled    bool
		Position   int
	}
	var rows []row
	for _, k := range enabled {
		d, _ := findSection(k)
		rows = append(rows, row{d.Key, d.Title, true, len(rows) + 1})
	}
	for _, d := range homeSectionDefs {
		if !slices.Contains(enabled, d.Key) {
			rows = append(rows, row{d.Key, d.Title, false, len(rows) + 1})
		}
	}
	s.page(w, r, "admin/homepage", map[string]any{"Title": "Homepage sections", "Rows": rows})
	return nil
}

func (s *Server) adminHomepageSave(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	type pos struct {
		key string
		n   int
	}
	var list []pos
	for _, d := range homeSectionDefs {
		if r.PostForm.Get("enabled_"+d.Key) == "1" {
			n, _ := strconv.Atoi(r.PostForm.Get("pos_" + d.Key))
			list = append(list, pos{d.Key, n})
		}
	}
	slices.SortStableFunc(list, func(a, b pos) int { return a.n - b.n })
	keys := make([]string, len(list))
	for i, p := range list {
		keys[i] = p.key
	}
	if err := s.settings.Save(r.Context(), map[string]string{"home_sections": strings.Join(keys, ",")}); err != nil {
		return err
	}
	s.audit(r, "settings.homepage", "settings", "", map[string]any{"sections": keys})
	s.flash(r, "Homepage layout saved.")
	redirect(w, r, "/admin/homepage")
	return nil
}

// ---------- product preview ----------

// adminProductPreview renders the storefront product page for any status (draft included).
func (s *Server) adminProductPreview(w http.ResponseWriter, r *http.Request) error {
	p, err := s.catalog.ProductByID(r.Context(), idParam(r, "id"))
	if err != nil {
		return err
	}
	related, err := s.catalog.Related(r.Context(), p, 4)
	if err != nil {
		return err
	}
	w.Header().Set("X-Robots-Tag", "noindex")
	s.page(w, r, "store/product", map[string]any{"Product": p, "Related": related, "Title": "Preview: " + p.Name,
		"Preview": true})
	return nil
}

// ---------- courier ----------

func (s *Server) courierData(r *http.Request, o *orders.Order) (map[string]any, error) {
	c, err := s.courier.Consignment(r.Context(), o.ID)
	if err != nil {
		return nil, err
	}
	events, err := s.courier.Events(r.Context(), o.ID)
	if err != nil {
		return nil, err
	}
	eligible := slices.Contains([]string{orders.StatusConfirmed, orders.StatusProcessing, orders.StatusReadyToShip}, o.Status)
	return map[string]any{"Configured": s.courier.Configured(), "Consignment": c, "Events": events,
		"Eligible": eligible, "AutoSend": s.courier.AutoSend}, nil
}

func (s *Server) adminCourierSend(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	c, err := s.courier.Send(r.Context(), adminFrom(r).ID, id, r.FormValue("note"))
	switch {
	case err == nil:
		s.audit(r, "courier.create", "order", id, map[string]any{"consignment_id": c.ConsignmentID, "tracking": c.TrackingCode})
		s.flash(r, fmt.Sprintf("Sent to Steadfast. Consignment #%d created.", *c.ConsignmentID))
	case errors.Is(err, orders.ErrNotFound):
		return err
	default:
		s.audit(r, "courier.create_failed", "order", id, map[string]any{"error": err.Error()})
		s.flash(r, "Steadfast: "+courierErrorMessage(err))
	}
	redirect(w, r, fmt.Sprintf("/admin/orders/%d#courier", id))
	return nil
}

func (s *Server) adminCourierSync(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	c, err := s.courier.Sync(r.Context(), id)
	if err != nil {
		s.flash(r, "Status refresh failed: "+courierErrorMessage(err))
	} else {
		s.flash(r, "Courier status: "+c.StatusLabel())
	}
	redirect(w, r, fmt.Sprintf("/admin/orders/%d#courier", id))
	return nil
}

func courierErrorMessage(err error) string {
	var se *steadfast.Error
	if errors.As(err, &se) && se.Ambiguous {
		return "Steadfast did not respond in time. Nothing was created on our side; check the Steadfast panel, then try again. (" + se.Message + ")"
	}
	return err.Error()
}

// adminCourierEligibility shows the customer's delivery history across Steadfast (fraud check).
func (s *Server) adminCourierEligibility(w http.ResponseWriter, r *http.Request) error {
	o, err := s.orders.OrderByID(r.Context(), idParam(r, "id"))
	if err != nil {
		return err
	}
	data := map[string]any{}
	if fc, err := s.courier.FraudCheck(r.Context(), o.Phone); err != nil {
		data["Error"] = courierErrorMessage(err)
	} else {
		data["Check"] = fc
	}
	s.render(w, r, http.StatusOK, "admin/order", "eligibility", data)
	return nil
}

func (s *Server) adminCourierBalance(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	bal, err := s.courier.Balance(r.Context())
	if err != nil {
		fmt.Fprintf(w, `<span class="text-rose-600">%s</span>`, html.EscapeString(courierErrorMessage(err)))
		return nil
	}
	fmt.Fprintf(w, `<span class="font-semibold">৳%.2f</span>`, bal)
	return nil
}

// ---------- account & admin users ----------

func (s *Server) adminAccount(w http.ResponseWriter, r *http.Request) error {
	s.page(w, r, "admin/account", map[string]any{"Title": "My account"})
	return nil
}

func (s *Server) adminAccountPassword(w http.ResponseWriter, r *http.Request) error {
	a := adminFrom(r)
	next := r.FormValue("new_password")
	if next != r.FormValue("confirm_password") {
		s.flash(r, "The new passwords do not match.")
	} else if err := s.auth.ChangeOwnPassword(r.Context(), a.ID, r.FormValue("current_password"), next); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			s.flash(r, "Your current password is incorrect.")
		} else if errors.Is(err, auth.ErrWeakPassword) {
			s.flash(r, err.Error())
		} else {
			return err
		}
	} else {
		// Rotate the session token after a credential change.
		if err := s.sessions.RenewToken(r.Context()); err != nil {
			return err
		}
		s.audit(r, "auth.password_change", "admin", a.ID, nil)
		s.flash(r, "Password changed.")
	}
	redirect(w, r, "/admin/account")
	return nil
}

func (s *Server) adminAdminUpdate(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	err := s.auth.Update(r.Context(), id, r.FormValue("name"), r.FormValue("role"), r.FormValue("active") == "1")
	if err == nil && r.FormValue("password") != "" {
		err = s.auth.SetPassword(r.Context(), id, r.FormValue("password"))
	}
	if err != nil {
		s.flash(r, "Could not update user: "+err.Error())
	} else {
		s.audit(r, "admin.update", "admin", id, map[string]any{"role": r.FormValue("role"), "active": r.FormValue("active") == "1",
			"password_reset": r.FormValue("password") != ""})
		s.flash(r, "User updated.")
	}
	redirect(w, r, "/admin/admins")
	return nil
}

// ---------- API docs ----------

func (s *Server) apiDocs(w http.ResponseWriter, r *http.Request) error {
	s.page(w, r, "store/apidocs", map[string]any{"Title": "API documentation"})
	return nil
}

func (s *Server) openAPISpec(w http.ResponseWriter, r *http.Request) {
	b, err := web.FS.ReadFile("static/openapi.yaml")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(b)
}
