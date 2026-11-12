package payment_test

// Integration tests against a real PostgreSQL database and a fake SSLCommerz server.
// Run with: TEST_DATABASE_URL=postgres://... go test ./internal/payment/

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuad71/ecommerce/internal/auth"
	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/db"
	"github.com/fuad71/ecommerce/internal/orders"
	"github.com/fuad71/ecommerce/internal/payment"
	"github.com/fuad71/ecommerce/internal/payment/sslcommerz"
	"github.com/fuad71/ecommerce/internal/settings"
)

// gateway fakes SSLCommerz: sessions record their tran_id, and validation answers with
// whatever amount the test sets for that val_id.
type gateway struct {
	mu       sync.Mutex
	srv      *httptest.Server
	sessions []string             // tran_ids
	valid    map[string][2]string // val_id -> {tran_id, amount}
}

func newGateway(t *testing.T) *gateway {
	g := &gateway{valid: map[string][2]string{}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch r.URL.Path {
		case "/gwprocess/v4/api.php":
			_ = r.ParseForm()
			g.sessions = append(g.sessions, r.PostForm.Get("tran_id"))
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "SUCCESS", "GatewayPageURL": g.srv.URL + "/pay/" + r.PostForm.Get("tran_id")})
		case "/validator/api/validationserverAPI.php":
			v, ok := g.valid[r.URL.Query().Get("val_id")]
			if !ok {
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "INVALID_TRANSACTION"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "VALID", "tran_id": v[0], "val_id": r.URL.Query().Get("val_id"),
				"amount": v[1], "currency": "BDT", "card_type": "BKASH-BKash", "bank_tran_id": "B1", "risk_level": "0"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// pay simulates the customer paying the latest session; it returns the val_id.
func (g *gateway) pay(amount string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	tran := g.sessions[len(g.sessions)-1]
	val := "VAL-" + tran
	g.valid[val] = [2]string{tran, amount}
	return val
}

type env struct {
	pool     *pgxpool.Pool
	orders   *orders.Service
	payments *payment.Service
	gw       *gateway
	catalog  *catalog.Service
	adminID  int64
	zoneID   int64
}

func setup(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE payments, courier_events, courier_consignments, offer_products, offer_categories, offers,
		inventory_movements, order_notes, order_status_history, order_items, orders, customers, product_images,
		product_variants, products, categories, audit_logs, sessions, admins RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE store_settings SET value = '0' WHERE key = 'free_delivery_threshold'`); err != nil {
		t.Fatal(err)
	}
	st := settings.NewService(pool)
	a, err := auth.NewService(pool).Create(ctx, "pay@example.com", "Tester", "test-password-123", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	zones, err := st.Zones(ctx, true)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	gw := newGateway(t)
	ord := orders.NewService(pool, st)
	return &env{pool: pool, orders: ord, gw: gw, catalog: catalog.NewService(pool), adminID: a.ID, zoneID: zones[0].ID,
		payments: payment.New(pool, ord, sslcommerz.New(gw.srv.URL, "store", "pass", 5*time.Second))}
}

// onlineOrder places an online-payment order for one unit priced ৳500 (+ zone delivery charge).
func (e *env) onlineOrder(t *testing.T, sku string) *orders.Order {
	t.Helper()
	ctx := context.Background()
	in := &catalog.ProductInput{Name: "Product " + sku, SKU: sku, RegularPrice: 50000, SellingPrice: 50000,
		Status: catalog.StatusActive, InitialStock: 5}
	if err := e.catalog.SaveProduct(ctx, e.adminID, in); err != nil {
		t.Fatal(err)
	}
	o, err := e.orders.PlaceOrder(ctx, orders.CheckoutInput{
		Name: "Rahim Uddin", Phone: "01712345678", District: "Dhaka", Area: "Mirpur", Address: "House 12, Road 5, Block C",
		ZoneID: e.zoneID, PaymentMethod: orders.PaymentOnline, OnlineAvailable: true, Confirmed: true,
		IdempotencyKey: "idem-" + sku + "-0000000000", Items: []orders.CartLine{{SKU: sku, Qty: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func (e *env) start(t *testing.T, o *orders.Order) string {
	t.Helper()
	u, err := e.payments.Start(context.Background(), o, payment.Callbacks{Success: "https://shop/s", Fail: "https://shop/f", Cancel: "https://shop/c", IPN: "https://shop/i"})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// plain formats poisha the way SSLCommerz reports amounts ("1234.00").
func plain(p int64) string { return fmt.Sprintf("%d.%02d", p/100, p%100) }

func TestOnlineOrderRequiresGateway(t *testing.T) {
	e := setup(t)
	_, err := e.orders.PlaceOrder(context.Background(), orders.CheckoutInput{
		Name: "Rahim Uddin", Phone: "01712345678", District: "Dhaka", Area: "Mirpur", Address: "House 12, Road 5, Block C",
		ZoneID: e.zoneID, PaymentMethod: orders.PaymentOnline, Confirmed: true, IdempotencyKey: "idem-no-gateway-000000",
		Items: []orders.CartLine{{SKU: "X", Qty: 1}},
	})
	var fe orders.FieldErrors
	if !errors.As(err, &fe) || fe["payment_method"] == "" {
		t.Fatalf("expected payment_method field error, got %v", err)
	}
}

func TestSuccessfulPaymentMarksOrderPaid(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	o := e.onlineOrder(t, "PAY-1")
	if !o.AwaitingPayment() || o.AmountDue() != o.Total {
		t.Fatalf("new online order: awaiting=%v due=%d total=%d", o.AwaitingPayment(), o.AmountDue(), o.Total)
	}
	// An unpaid online order can't be confirmed.
	if err := e.orders.UpdateStatus(ctx, e.adminID, o.ID, orders.StatusConfirmed, ""); !errors.Is(err, orders.ErrAwaitingPayment) {
		t.Fatalf("confirm unpaid: %v", err)
	}

	if u := e.start(t, o); !strings.HasPrefix(u, e.gw.srv.URL+"/pay/"+o.Number+"-") {
		t.Fatalf("gateway url = %s", u)
	}
	val := e.gw.pay(plain(o.Total))
	id, err := e.payments.Complete(ctx, val)
	if err != nil || id != o.ID {
		t.Fatalf("complete: id=%d err=%v", id, err)
	}
	// The IPN arriving too must be a no-op.
	if _, err := e.payments.Complete(ctx, val); err != nil {
		t.Fatalf("second complete: %v", err)
	}

	got, _ := e.orders.OrderByID(ctx, o.ID)
	if !got.IsPaid() || got.PaidAt == nil || got.AmountDue() != 0 || got.AwaitingPayment() {
		t.Fatalf("after payment: status=%s paid_at=%v due=%d", got.PaymentStatus, got.PaidAt, got.AmountDue())
	}
	ps, _ := e.payments.ForOrder(ctx, o.ID)
	if len(ps) != 1 || ps[0].Status != "paid" || ps[0].CardType != "BKASH-BKash" {
		t.Fatalf("payments = %+v", ps)
	}
	if err := e.orders.UpdateStatus(ctx, e.adminID, o.ID, orders.StatusConfirmed, ""); err != nil {
		t.Fatalf("confirm paid order: %v", err)
	}
	if _, err := e.payments.Start(ctx, got, payment.Callbacks{}); !errors.Is(err, payment.ErrNotPayable) {
		t.Fatalf("start on paid order: %v", err)
	}
}

func TestPaymentAmountMustMatch(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	o := e.onlineOrder(t, "PAY-2")
	e.start(t, o)
	if _, err := e.payments.Complete(ctx, e.gw.pay("1.00")); !errors.Is(err, payment.ErrMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
	if _, err := e.payments.Complete(ctx, "forged-val-id"); !errors.Is(err, payment.ErrNotPaid) {
		t.Fatalf("expected not paid for unknown val_id, got %v", err)
	}
	got, _ := e.orders.OrderByID(ctx, o.ID)
	ps, _ := e.payments.ForOrder(ctx, o.ID)
	if got.IsPaid() || len(ps) != 1 || ps[0].Status != "failed" || !strings.Contains(ps[0].LastError, "amount mismatch") {
		t.Fatalf("paid=%v payments=%+v", got.IsPaid(), ps)
	}
}

func TestRetryAndDuplicatePayment(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	o := e.onlineOrder(t, "PAY-3")
	e.start(t, o)
	first := e.gw.pay(plain(o.Total))
	e.start(t, o) // customer opens a second attempt in another tab
	second := e.gw.pay(plain(o.Total))
	if _, err := e.payments.Complete(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := e.payments.Complete(ctx, second); err != nil {
		t.Fatal(err)
	}
	ps, _ := e.payments.ForOrder(ctx, o.ID)
	paid := 0
	for _, p := range ps {
		if p.Status == "paid" {
			paid++
		}
	}
	notes, _ := e.orders.Notes(ctx, o.ID)
	if paid != 1 || len(notes) != 1 || !strings.Contains(notes[0].Body, "Duplicate") {
		t.Fatalf("paid=%d notes=%+v", paid, notes)
	}
}

func TestUnpaidOrdersExpireAndLatePaymentIsFlagged(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	o := e.onlineOrder(t, "PAY-4")
	e.start(t, o)
	if _, err := e.pool.Exec(ctx, `UPDATE orders SET created_at = now() - interval '2 hours' WHERE id = $1`, o.ID); err != nil {
		t.Fatal(err)
	}
	n, err := e.orders.ExpireUnpaidOnline(ctx, time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("expire: n=%d err=%v", n, err)
	}
	var reserved int
	_ = e.pool.QueryRow(ctx, `SELECT reserved FROM product_variants WHERE sku = 'PAY-4'`).Scan(&reserved)
	got, _ := e.orders.OrderByID(ctx, o.ID)
	if got.Status != orders.StatusCancelled || reserved != 0 {
		t.Fatalf("status=%s reserved=%d", got.Status, reserved)
	}

	// The customer finishes paying after all: record it and flag the refund.
	if _, err := e.payments.Complete(ctx, e.gw.pay(plain(o.Total))); err != nil {
		t.Fatal(err)
	}
	got, _ = e.orders.OrderByID(ctx, o.ID)
	notes, _ := e.orders.Notes(ctx, o.ID)
	if !got.IsPaid() || len(notes) != 1 || !strings.Contains(notes[0].Body, "Refund") {
		t.Fatalf("paid=%v notes=%+v", got.IsPaid(), notes)
	}
}
