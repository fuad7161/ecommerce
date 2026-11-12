package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/fuad71/ecommerce/internal/accounts"
	"github.com/fuad71/ecommerce/internal/orders"
)

// Customer accounts are optional. Guests check out and track orders exactly as before;
// a logged-in customer additionally sees every order placed while logged in, plus guest
// orders they add by proving they know the order number and checkout phone.
func (s *Server) accountRoutes(r chi.Router) {
	r.Get("/login", s.h(s.accountLoginPage))
	r.With(s.rateLimit("account-login", 10, 5*time.Minute)).Post("/login", s.h(s.accountLoginSubmit))
	r.Get("/register", s.h(s.accountRegisterPage))
	r.With(s.rateLimit("account-register", 10, time.Hour)).Post("/register", s.h(s.accountRegisterSubmit))

	r.Group(func(r chi.Router) {
		r.Use(s.requireCustomer)
		r.Post("/logout", s.h(s.accountLogout))
		r.Get("/", s.h(s.accountOrders))
		r.Get("/orders/{number}", s.h(s.accountOrder))
		// Same limit as tracking: claiming is another way to test number + phone pairs.
		r.With(s.rateLimit("account-claim", 15, time.Minute)).Post("/orders/claim", s.h(s.accountClaim))
		r.Post("/orders/attach", s.h(s.accountAttach))
		r.Get("/profile", s.h(s.accountProfile))
		r.Post("/profile", s.h(s.accountProfileSave))
		r.Post("/password", s.h(s.accountPassword))
	})
}

// safeNext only allows same-site paths, so ?next= can't be used as an open redirect.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.ContainsAny(next, "\r\n") || strings.HasPrefix(next, "/account/log") {
		return "/account"
	}
	return next
}

// startCustomerSession logs the account in on a fresh session token (prevents fixation).
func (s *Server) startCustomerSession(r *http.Request, a *accounts.Account) error {
	ctx := r.Context()
	if err := s.customerSessions.RenewToken(ctx); err != nil {
		return err
	}
	s.customerSessions.Put(ctx, "account_id", a.ID)
	s.customerSessions.Put(ctx, "account_epoch", a.SessionEpoch)
	return nil
}

func (s *Server) customerFlash(r *http.Request, msg string) {
	s.customerSessions.Put(r.Context(), "flash", msg)
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request, status int, page string, data map[string]any) {
	if _, ok := data["Flash"]; !ok {
		data["Flash"] = s.customerSessions.PopString(r.Context(), "flash")
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, status, page, "layout", data)
}

// ---------- login / register ----------

func (s *Server) accountLoginPage(w http.ResponseWriter, r *http.Request) error {
	next := safeNext(r.URL.Query().Get("next"))
	if customerFrom(r) != nil {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return nil
	}
	s.accountPage(w, r, http.StatusOK, "store/account_login", map[string]any{"Title": "Log in", "Next": next})
	return nil
}

func (s *Server) accountLoginSubmit(w http.ResponseWriter, r *http.Request) error {
	email, password := r.FormValue("email"), r.FormValue("password")
	next := safeNext(r.FormValue("next"))
	a, err := s.accounts.Authenticate(r.Context(), email, password)
	if errors.Is(err, accounts.ErrInvalidCredentials) {
		slog.InfoContext(r.Context(), "customer login failed", "ip", clientIP(r))
		s.accountPage(w, r, http.StatusUnauthorized, "store/account_login", map[string]any{
			"Title": "Log in", "Error": err.Error(), "Email": email, "Next": next,
		})
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.startCustomerSession(r, a); err != nil {
		return err
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
	return nil
}

func (s *Server) accountRegisterPage(w http.ResponseWriter, r *http.Request) error {
	next := safeNext(r.URL.Query().Get("next"))
	if customerFrom(r) != nil {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return nil
	}
	s.accountPage(w, r, http.StatusOK, "store/account_register", map[string]any{
		"Title": "Create an account", "Next": next, "Form": map[string]string{}, "Errors": accounts.FieldErrors{},
	})
	return nil
}

func (s *Server) accountRegisterSubmit(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	f := r.PostForm
	next := safeNext(f.Get("next"))
	in := accounts.RegisterInput{Name: f.Get("name"), Email: f.Get("email"), Phone: f.Get("phone"), Password: f.Get("password")}
	var (
		a   *accounts.Account
		err error
		fe  accounts.FieldErrors
	)
	if f.Get("password") != f.Get("password_confirm") {
		err = accounts.FieldErrors{"password_confirm": "Passwords don't match"}
	} else {
		a, err = s.accounts.Register(r.Context(), in)
	}
	if errors.As(err, &fe) {
		s.accountPage(w, r, http.StatusUnprocessableEntity, "store/account_register", map[string]any{
			"Title": "Create an account", "Next": next, "Errors": fe,
			"Form": map[string]string{"name": in.Name, "email": in.Email, "phone": in.Phone},
		})
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.startCustomerSession(r, a); err != nil {
		return err
	}
	slog.InfoContext(r.Context(), "customer registered", "account", a.ID)
	s.customerFlash(r, "Welcome, "+a.FirstName()+"! Your account is ready.")
	http.Redirect(w, r, next, http.StatusSeeOther)
	return nil
}

func (s *Server) accountLogout(w http.ResponseWriter, r *http.Request) error {
	if err := s.customerSessions.Destroy(r.Context()); err != nil {
		return err
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}

// ---------- orders ----------

func (s *Server) accountOrders(w http.ResponseWriter, r *http.Request) error {
	return s.renderAccountOrders(w, r, http.StatusOK, map[string]any{})
}

func (s *Server) renderAccountOrders(w http.ResponseWriter, r *http.Request, status int, data map[string]any) error {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	list, err := s.orders.ListForAccount(r.Context(), customerFrom(r).ID, page)
	if err != nil {
		return err
	}
	data["Title"] = "My orders"
	data["Orders"] = list
	data["Tab"] = "orders"
	s.accountPage(w, r, status, "store/account_orders", data)
	return nil
}

func (s *Server) accountOrder(w http.ResponseWriter, r *http.Request) error {
	o, err := s.orders.ForAccount(r.Context(), customerFrom(r).ID, chi.URLParam(r, "number"))
	if err != nil {
		return err
	}
	data := map[string]any{"Title": "Order " + o.Number, "Order": o, "Tab": "orders"}
	if o.AwaitingPayment() && s.onlineAvailable(r.Context()) {
		data["PayURL"] = s.orderURL(o.Number, "")
	}
	s.accountPage(w, r, http.StatusOK, "store/account_order", data)
	return nil
}

// accountClaim adds a guest order to the account. It needs the order number AND the phone
// used at checkout, and answers "not found" for every failure so pairs can't be probed.
func (s *Server) accountClaim(w http.ResponseWriter, r *http.Request) error {
	number, ph := r.FormValue("order"), r.FormValue("phone")
	o, err := s.orders.ClaimGuestOrder(r.Context(), customerFrom(r).ID, number, ph)
	switch {
	case err == nil:
		slog.InfoContext(r.Context(), "order added to account", "order", o.Number, "account", customerFrom(r).ID)
		s.customerFlash(r, "Order "+o.Number+" was added to your account.")
		http.Redirect(w, r, "/account/orders/"+o.Number, http.StatusSeeOther)
		return nil
	case errors.Is(err, orders.ErrAlreadyInAccount):
		s.customerFlash(r, "That order is already in your account.")
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return nil
	case errors.Is(err, orders.ErrNotFound):
		return s.renderAccountOrders(w, r, http.StatusUnprocessableEntity, map[string]any{
			"ClaimError": "We couldn't find an order with that number and phone. Please check both and try again.",
			"ClaimOrder": number, "ClaimPhone": ph,
		})
	}
	return err
}

// accountAttach adds the order shown on a confirmation page; its signed link is the proof.
func (s *Server) accountAttach(w http.ResponseWriter, r *http.Request) error {
	number := r.FormValue("order")
	if !s.validOrderToken(number, r.FormValue("t")) {
		return orders.ErrNotFound
	}
	_, err := s.orders.AttachOrder(r.Context(), customerFrom(r).ID, number)
	if err != nil && !errors.Is(err, orders.ErrAlreadyInAccount) {
		return err // ErrNotFound (e.g. already in another account) renders the 404 page
	}
	s.customerFlash(r, "Order "+number+" is saved in your account.")
	http.Redirect(w, r, "/account/orders/"+number, http.StatusSeeOther)
	return nil
}

// ---------- profile ----------

func (s *Server) accountProfile(w http.ResponseWriter, r *http.Request) error {
	c := customerFrom(r)
	s.accountPage(w, r, http.StatusOK, "store/account_profile", map[string]any{
		"Title": "My profile", "Tab": "profile", "Errors": accounts.FieldErrors{},
		"Form": map[string]string{"name": c.Name, "phone": c.Phone, "district": c.District, "area": c.Area, "address": c.Address},
	})
	return nil
}

func (s *Server) accountProfileSave(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	f := r.PostForm
	in := accounts.ProfileInput{Name: f.Get("name"), Phone: f.Get("phone"), District: f.Get("district"),
		Area: f.Get("area"), Address: f.Get("address")}
	err := s.accounts.UpdateProfile(r.Context(), customerFrom(r).ID, in)
	var fe accounts.FieldErrors
	if errors.As(err, &fe) {
		s.accountPage(w, r, http.StatusUnprocessableEntity, "store/account_profile", map[string]any{
			"Title": "My profile", "Tab": "profile", "Errors": fe,
			"Form": map[string]string{"name": in.Name, "phone": in.Phone, "district": in.District, "area": in.Area, "address": in.Address},
		})
		return nil
	}
	if err != nil {
		return err
	}
	s.customerFlash(r, "Your profile was saved.")
	http.Redirect(w, r, "/account/profile", http.StatusSeeOther)
	return nil
}

func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) error {
	c := customerFrom(r)
	var (
		epoch int
		err   error
		fe    accounts.FieldErrors
	)
	if r.FormValue("new_password") != r.FormValue("new_password_confirm") {
		err = accounts.FieldErrors{"new_password_confirm": "Passwords don't match"}
	} else {
		epoch, err = s.accounts.ChangePassword(r.Context(), c.ID, r.FormValue("current_password"), r.FormValue("new_password"))
	}
	if errors.As(err, &fe) {
		s.accountPage(w, r, http.StatusUnprocessableEntity, "store/account_profile", map[string]any{
			"Title": "My profile", "Tab": "profile", "Errors": accounts.FieldErrors{}, "PasswordErrors": fe,
			"Form": map[string]string{"name": c.Name, "phone": c.Phone, "district": c.District, "area": c.Area, "address": c.Address},
		})
		return nil
	}
	if err != nil {
		return err
	}
	// Other devices are now signed out; keep this one signed in on a fresh token.
	if err := s.customerSessions.RenewToken(r.Context()); err != nil {
		return err
	}
	s.customerSessions.Put(r.Context(), "account_epoch", epoch)
	s.customerFlash(r, "Password changed. Other devices have been signed out.")
	http.Redirect(w, r, "/account/profile", http.StatusSeeOther)
	return nil
}
