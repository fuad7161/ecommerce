package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/fuad71/ecommerce/internal/orders"
	"github.com/fuad71/ecommerce/internal/payment"
)

// onlineAvailable reports whether customers can choose online payment right now.
func (s *Server) onlineAvailable(ctx context.Context) bool {
	if !s.payments.Configured() {
		return false
	}
	st, err := s.settings.All(ctx)
	return err == nil && st.Get("online_payment_enabled") != "false"
}

// orderURL is the customer's confirmation page, signed so only they can open it.
func (s *Server) orderURL(number, pay string) string {
	u := "/order/" + number + "?t=" + s.orderToken(number)
	if pay != "" {
		u += "&pay=" + url.QueryEscape(pay)
	}
	return u
}

// redirectToGateway opens an SSLCommerz session and sends the customer there. If the
// gateway is unreachable the customer lands on their order page with a retry button.
func (s *Server) redirectToGateway(w http.ResponseWriter, r *http.Request, o *orders.Order) {
	q := "?order=" + url.QueryEscape(o.Number) + "&t=" + s.orderToken(o.Number)
	base := s.cfg.AppURL + "/payment/sslcommerz"
	gw, err := s.payments.Start(r.Context(), o, payment.Callbacks{
		Success: base + "/return/success" + q,
		Fail:    base + "/return/fail" + q,
		Cancel:  base + "/return/cancel" + q,
		IPN:     base + "/ipn",
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "start online payment", "err", err, "order", o.Number)
		http.Redirect(w, r, s.orderURL(o.Number, "error"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, gw, http.StatusSeeOther)
}

// orderPay lets the customer retry an unpaid online order from its confirmation page.
func (s *Server) orderPay(w http.ResponseWriter, r *http.Request) error {
	number := chi.URLParam(r, "number")
	if err := r.ParseForm(); err != nil {
		return err
	}
	if !s.validOrderToken(number, r.PostForm.Get("t")) {
		return orders.ErrNotFound
	}
	o, err := s.orders.OrderByNumber(r.Context(), number)
	if err != nil {
		return err
	}
	if !o.AwaitingPayment() || !s.onlineAvailable(r.Context()) {
		http.Redirect(w, r, s.orderURL(o.Number, ""), http.StatusSeeOther)
		return nil
	}
	s.redirectToGateway(w, r, o)
	return nil
}

// paymentReturn handles the customer's browser coming back from SSLCommerz.
func (s *Server) paymentReturn(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	number := r.URL.Query().Get("order")
	if !s.validOrderToken(number, r.URL.Query().Get("t")) {
		return orders.ErrNotFound
	}
	o, err := s.orders.OrderByNumber(r.Context(), number)
	if err != nil {
		return err
	}
	tranID := r.PostForm.Get("tran_id")
	ours := strings.HasPrefix(tranID, o.Number+"-")

	result := "failed"
	switch chi.URLParam(r, "outcome") {
	case "success":
		id, err := s.payments.Complete(r.Context(), r.PostForm.Get("val_id"))
		switch {
		case err == nil && id == o.ID:
			result = "success"
		case err == nil:
			slog.WarnContext(r.Context(), "payment return for a different order", "order", o.Number, "tran_id", tranID)
		case errors.Is(err, payment.ErrNotPaid), errors.Is(err, payment.ErrMismatch):
			slog.WarnContext(r.Context(), "payment return not valid", "err", err, "order", o.Number)
		default:
			// Gateway unreachable: the IPN will settle it; tell the customer we're checking.
			slog.ErrorContext(r.Context(), "validate payment", "err", err, "order", o.Number)
			result = "pending"
		}
	case "cancel":
		result = "cancelled"
		if ours {
			s.payments.Abandon(r.Context(), tranID, "CANCELLED")
		}
	default:
		if ours {
			s.payments.Abandon(r.Context(), tranID, "FAILED")
		}
	}
	http.Redirect(w, r, s.orderURL(o.Number, result), http.StatusSeeOther)
	return nil
}

// paymentIPN is SSLCommerz's server-to-server notification. It is the reliable signal: it
// arrives even when the customer closes the browser before returning to the store.
func (s *Server) paymentIPN(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	status := strings.ToUpper(r.PostForm.Get("status"))
	tranID := r.PostForm.Get("tran_id")
	switch status {
	case "VALID", "VALIDATED":
		if _, err := s.payments.Complete(r.Context(), r.PostForm.Get("val_id")); err != nil {
			if errors.Is(err, payment.ErrNotPaid) || errors.Is(err, payment.ErrMismatch) {
				slog.WarnContext(r.Context(), "ipn rejected", "err", err, "tran_id", tranID)
				break
			}
			slog.ErrorContext(r.Context(), "ipn validate", "err", err, "tran_id", tranID)
			http.Error(w, "try again", http.StatusInternalServerError) // SSLCommerz retries
			return
		}
	case "FAILED", "CANCELLED", "EXPIRED", "UNATTEMPTED":
		s.payments.Abandon(r.Context(), tranID, status)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}
