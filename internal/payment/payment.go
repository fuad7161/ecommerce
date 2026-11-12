// Package payment handles online payments for orders through SSLCommerz (bKash, Nagad,
// Rocket, cards and internet banking behind one hosted checkout page).
//
// An order paid online is created like any other order (stock reserved, status pending) with
// payment_method 'online' and payment_status 'unpaid'. Each trip to the gateway is a row in
// payments. The order becomes paid only after SSLCommerz's validation API confirms the
// transaction and its amount; browser redirects and IPN posts are just triggers for that check.
package payment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuad71/ecommerce/internal/db"
	"github.com/fuad71/ecommerce/internal/money"
	"github.com/fuad71/ecommerce/internal/orders"
	"github.com/fuad71/ecommerce/internal/payment/sslcommerz"
)

var (
	ErrNotConfigured = errors.New("online payment is not configured")
	ErrNotPayable    = errors.New("this order cannot be paid online")
	ErrNotPaid       = errors.New("the payment was not completed")
	ErrMismatch      = errors.New("the payment does not match the order")
)

type Payment struct {
	ID         int64
	TranID     string
	Amount     int64
	Status     string
	ValID      string
	BankTranID string
	CardType   string
	RiskLevel  string
	RiskTitle  string
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Risky reports whether SSLCommerz flagged the transaction for manual verification.
func (p Payment) Risky() bool { return p.RiskLevel == "1" }

type Service struct {
	pool   *pgxpool.Pool
	orders *orders.Service
	client *sslcommerz.Client
}

// New returns a payment service. client may be nil when the gateway is not configured.
func New(pool *pgxpool.Pool, ord *orders.Service, client *sslcommerz.Client) *Service {
	return &Service{pool: pool, orders: ord, client: client}
}

func (s *Service) Configured() bool { return s.client != nil }

// GatewayOrigin is the gateway's base URL, or "" when not configured.
func (s *Service) GatewayOrigin() string {
	if s.client == nil {
		return ""
	}
	return s.client.BaseURL()
}

// Callbacks are the absolute URLs SSLCommerz sends the customer (and its IPN) back to.
type Callbacks struct {
	Success, Fail, Cancel, IPN string
}

// Start opens a gateway session for an unpaid online order and returns the URL to send the
// customer to.
func (s *Service) Start(ctx context.Context, o *orders.Order, cb Callbacks) (string, error) {
	if s.client == nil {
		return "", ErrNotConfigured
	}
	if !o.AwaitingPayment() {
		return "", ErrNotPayable
	}
	tranID := o.Number + "-" + randomHex(6)
	if _, err := s.pool.Exec(ctx, `INSERT INTO payments (order_id, tran_id, amount) VALUES ($1, $2, $3)`,
		o.ID, tranID, o.Total); err != nil {
		return "", err
	}

	names := make([]string, 0, len(o.Items))
	qty := 0
	for _, it := range o.Items {
		names = append(names, it.ProductName)
		qty += it.Quantity
	}
	sess, raw, err := s.client.CreateSession(ctx, sslcommerz.SessionRequest{
		TranID: tranID, Amount: money.Plain(o.Total),
		SuccessURL: cb.Success, FailURL: cb.Fail, CancelURL: cb.Cancel, IPNURL: cb.IPN,
		CustName: o.CustomerName, CustPhone: o.Phone,
		CustAddress: o.Address + ", " + o.Area, CustCity: o.District,
		NumItems: qty, ProductName: strings.Join(names, ", "),
	})
	if err != nil {
		s.markAttempt(ctx, tranID, "failed", err.Error(), raw)
		return "", err
	}
	s.markAttempt(ctx, tranID, "initiated", "", raw)
	return sess.GatewayURL, nil
}

// Complete validates val_id with SSLCommerz and, when genuine and matching, marks the order
// paid. It is idempotent and safe to call from both the browser return and the IPN.
// It returns the ID of the order the payment belongs to.
func (s *Service) Complete(ctx context.Context, valID string) (int64, error) {
	if s.client == nil {
		return 0, ErrNotConfigured
	}
	valID = strings.TrimSpace(valID)
	if valID == "" || len(valID) > 100 {
		return 0, ErrNotPaid
	}
	v, raw, err := s.client.Validate(ctx, valID)
	if err != nil {
		return 0, err
	}
	if !v.OK() {
		return 0, ErrNotPaid
	}

	var orderID int64
	var mismatch string
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var p Payment
		err := tx.QueryRow(ctx, `SELECT id, order_id, amount, status FROM payments WHERE tran_id = $1 FOR UPDATE`, v.TranID).
			Scan(&p.ID, &orderID, &p.Amount, &p.Status)
		if db.IsNoRows(err) {
			return fmt.Errorf("%w: unknown transaction %q", ErrMismatch, v.TranID)
		}
		if err != nil {
			return err
		}
		if p.Status == "paid" {
			return nil // already processed (IPN and browser return both arrive)
		}
		paid, err := money.Parse(v.Amount)
		if err != nil || paid != p.Amount || v.Currency != "BDT" {
			mismatch = fmt.Sprintf("amount mismatch: got %s %s, expected %s", v.Amount, v.Currency, money.Plain(p.Amount))
			_, err := tx.Exec(ctx, `UPDATE payments SET status = 'failed', last_error = $2, gateway_response = $3, updated_at = now()
				WHERE id = $1`, p.ID, mismatch, jsonOrNull(raw))
			return err // commit the failed attempt; the mismatch is reported below
		}

		var status, payStatus string
		if err := tx.QueryRow(ctx, `SELECT status, payment_status FROM orders WHERE id = $1 FOR UPDATE`, orderID).
			Scan(&status, &payStatus); err != nil {
			return err
		}
		newStatus, lastErr := "paid", ""
		var notes []string
		if payStatus == "paid" {
			// Customer paid twice (e.g. two tabs). Keep the first; flag this one for refund.
			newStatus, lastErr = "failed", "duplicate payment, refund it from the SSLCommerz panel"
			notes = append(notes, fmt.Sprintf("Duplicate online payment %s (%s) received. Refund it from the SSLCommerz merchant panel.",
				v.TranID, money.Format(paid)))
		} else {
			if _, err := tx.Exec(ctx, `UPDATE orders SET payment_status = 'paid', paid_at = now(), updated_at = now() WHERE id = $1`,
				orderID); err != nil {
				return err
			}
			if status == orders.StatusCancelled || status == orders.StatusReturned {
				notes = append(notes, fmt.Sprintf("Online payment %s (%s) arrived after the order was %s. Refund it or reinstate the order.",
					v.TranID, money.Format(paid), orders.StatusLabel(status)))
			}
		}
		if v.RiskLevel == "1" {
			notes = append(notes, "SSLCommerz flagged this payment as risky ("+v.RiskTitle+"). Verify the customer before shipping.")
		}
		if _, err := tx.Exec(ctx, `UPDATE payments SET status = $2, val_id = $3, bank_tran_id = $4, card_type = $5,
				risk_level = $6, risk_title = $7, last_error = $8, gateway_response = $9, updated_at = now() WHERE id = $1`,
			p.ID, newStatus, v.ValID, v.BankTranID, v.CardType, v.RiskLevel, v.RiskTitle, lastErr, jsonOrNull(raw)); err != nil {
			return err
		}
		for _, n := range notes {
			if _, err := tx.Exec(ctx, `INSERT INTO order_notes (order_id, body) VALUES ($1, $2)`, orderID, n); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if mismatch != "" {
		slog.WarnContext(ctx, "online payment rejected", "tran_id", v.TranID, "reason", mismatch)
		return 0, fmt.Errorf("%w: %s", ErrMismatch, mismatch)
	}
	slog.InfoContext(ctx, "online payment validated", "tran_id", v.TranID, "amount", v.Amount, "card_type", v.CardType)
	return orderID, nil
}

// Abandon records that the customer failed or cancelled at the gateway. It never touches a
// payment that has already been validated.
func (s *Service) Abandon(ctx context.Context, tranID, status string) {
	st := "failed"
	if strings.EqualFold(status, "CANCELLED") {
		st = "cancelled"
	}
	if _, err := s.pool.Exec(ctx, `UPDATE payments SET status = $2, last_error = $3, updated_at = now()
		WHERE tran_id = $1 AND status = 'initiated'`, tranID, st, strings.ToLower(status)); err != nil {
		slog.ErrorContext(ctx, "record abandoned payment", "err", err, "tran_id", tranID)
	}
}

// ForOrder lists an order's payment attempts, newest first.
func (s *Service) ForOrder(ctx context.Context, orderID int64) ([]Payment, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, tran_id, amount, status, val_id, bank_tran_id, card_type, risk_level,
			risk_title, last_error, created_at, updated_at
		FROM payments WHERE order_id = $1 ORDER BY id DESC`, orderID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Payment, error) {
		var p Payment
		err := r.Scan(&p.ID, &p.TranID, &p.Amount, &p.Status, &p.ValID, &p.BankTranID, &p.CardType, &p.RiskLevel,
			&p.RiskTitle, &p.LastError, &p.CreatedAt, &p.UpdatedAt)
		return p, err
	})
}

// RunExpiry periodically cancels online orders left unpaid for longer than timeout, so their
// reserved stock is released. A payment that arrives later is still recorded and flagged.
func (s *Service) RunExpiry(ctx context.Context, every, timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := s.orders.ExpireUnpaidOnline(ctx, timeout); err != nil {
			slog.ErrorContext(ctx, "expire unpaid orders", "err", err)
		} else if n > 0 {
			slog.InfoContext(ctx, "cancelled unpaid online orders", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) markAttempt(ctx context.Context, tranID, status, lastErr string, raw []byte) {
	if _, err := s.pool.Exec(ctx, `UPDATE payments SET status = $2, last_error = $3, gateway_response = $4, updated_at = now()
		WHERE tran_id = $1`, tranID, status, lastErr, jsonOrNull(raw)); err != nil {
		slog.ErrorContext(ctx, "record payment attempt", "err", err, "tran_id", tranID)
	}
}

// jsonOrNull stores gateway bodies only when they are valid JSON (the column is JSONB).
func jsonOrNull(raw []byte) any {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil
	}
	return string(raw)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
