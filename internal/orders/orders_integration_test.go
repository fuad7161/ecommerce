package orders_test

// Integration tests against a real PostgreSQL database.
// Run with: TEST_DATABASE_URL=postgres://... go test ./internal/orders/
// (`make test-integration` starts one with docker compose.)

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuad71/ecommerce/internal/auth"
	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/db"
	"github.com/fuad71/ecommerce/internal/orders"
	"github.com/fuad71/ecommerce/internal/settings"
)

type env struct {
	pool    *pgxpool.Pool
	orders  *orders.Service
	catalog *catalog.Service
	adminID int64
	zoneID  int64
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
	_, err = pool.Exec(ctx, `TRUNCATE courier_events, courier_consignments, offer_products, offer_categories, offers, inventory_movements, order_notes, order_status_history, order_items, orders,
		customers, product_images, product_variants, products, categories, audit_logs, sessions, admins RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE store_settings SET value = '0' WHERE key = 'free_delivery_threshold'`); err != nil {
		t.Fatal(err)
	}
	st := settings.NewService(pool)
	a, err := auth.NewService(pool).Create(ctx, "test@example.com", "Tester", "test-password-123", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	zones, err := st.Zones(ctx, true)
	if err != nil || len(zones) == 0 {
		t.Fatalf("zones: %v", err)
	}
	return &env{pool: pool, orders: orders.NewService(pool, st), catalog: catalog.NewService(pool), adminID: a.ID, zoneID: zones[0].ID}
}

// product creates an active product with one default variant and returns its SKU.
func (e *env) product(t *testing.T, sku string, regular, selling int64, stock int) string {
	t.Helper()
	in := &catalog.ProductInput{Name: "Product " + sku, SKU: sku, RegularPrice: regular, SellingPrice: selling,
		Status: catalog.StatusActive, InitialStock: stock}
	if err := e.catalog.SaveProduct(context.Background(), e.adminID, in); err != nil {
		t.Fatal(err)
	}
	return in.SKU
}

func (e *env) stock(t *testing.T, sku string) (stock, reserved int) {
	t.Helper()
	err := e.pool.QueryRow(context.Background(), `SELECT stock, reserved FROM product_variants WHERE sku = $1`, sku).Scan(&stock, &reserved)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func (e *env) checkout(key string, items ...orders.CartLine) orders.CheckoutInput {
	return orders.CheckoutInput{
		Name: "Rahim Uddin", Phone: "+880 1712-345678", District: "Dhaka", Area: "Mirpur",
		Address: "House 12, Road 5, Block C", ZoneID: e.zoneID, PaymentMethod: "cod", Confirmed: true,
		IdempotencyKey: key, Items: items,
	}
}

func TestPlaceOrderComputesTotalsServerSide(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	sku := e.product(t, "TEE-1", 100000, 80000, 10)

	o, err := e.orders.PlaceOrder(ctx, e.checkout("key-happy-path-0001", orders.CartLine{SKU: sku, Qty: 2}))
	if err != nil {
		t.Fatal(err)
	}
	// Inside Dhaka zone is ৳70 from the migration seed.
	if o.Subtotal != 200000 || o.Discount != 40000 || o.DeliveryCharge != 7000 || o.Total != 167000 {
		t.Fatalf("totals: %+v", o)
	}
	if o.Phone != "01712345678" || o.Status != orders.StatusPending || len(o.Items) != 1 || o.Items[0].UnitPrice != 80000 {
		t.Fatalf("order: %+v", o)
	}
	if st, res := e.stock(t, sku); st != 10 || res != 2 {
		t.Fatalf("stock=%d reserved=%d, want 10/2", st, res)
	}
}

func TestPlaceOrderValidation(t *testing.T) {
	e := setup(t)
	sku := e.product(t, "VAL-1", 1000, 1000, 5)
	in := e.checkout("key-validation-0001", orders.CartLine{SKU: sku, Qty: 1})
	in.Phone = "01212345678"
	in.Confirmed = false
	_, err := e.orders.PlaceOrder(context.Background(), in)
	var fe orders.FieldErrors
	if !errors.As(err, &fe) || fe["phone"] == "" || fe["confirm"] == "" {
		t.Fatalf("expected phone and confirm errors, got %v", err)
	}
}

func TestPlaceOrderIsIdempotent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	sku := e.product(t, "IDEM-1", 5000, 5000, 10)
	in := e.checkout("key-idempotent-0001", orders.CartLine{SKU: sku, Qty: 3})

	var wg sync.WaitGroup
	results := make([]*orders.Order, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o, err := e.orders.PlaceOrder(ctx, in)
			if err != nil {
				t.Error(err)
				return
			}
			results[i] = o
		}(i)
	}
	wg.Wait()
	for _, o := range results {
		if o == nil || o.Number != results[0].Number {
			t.Fatalf("duplicate submissions produced different orders")
		}
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n)
	if _, res := e.stock(t, sku); n != 1 || res != 3 {
		t.Fatalf("orders=%d reserved=%d, want 1/3", n, res)
	}
}

func TestConcurrentCheckoutNeverOversells(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	sku := e.product(t, "LAST-5", 2000, 2000, 5)

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, changed := 0, 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := e.orders.PlaceOrder(ctx, e.checkout(fmt.Sprintf("key-concurrent-%04d", i), orders.CartLine{SKU: sku, Qty: 1}))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, orders.ErrCartChanged):
				changed++
			default:
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if ok != 5 || changed != 15 {
		t.Fatalf("succeeded=%d rejected=%d, want 5/15", ok, changed)
	}
	if st, res := e.stock(t, sku); st != 5 || res != 5 {
		t.Fatalf("stock=%d reserved=%d, want 5/5", st, res)
	}
}

func TestOutOfStockRejected(t *testing.T) {
	e := setup(t)
	sku := e.product(t, "OOS-1", 1000, 1000, 1)
	_, err := e.orders.PlaceOrder(context.Background(), e.checkout("key-out-of-stock-01", orders.CartLine{SKU: sku, Qty: 2}))
	if !errors.Is(err, orders.ErrCartChanged) {
		t.Fatalf("want ErrCartChanged, got %v", err)
	}
	q, err := e.orders.Quote(context.Background(), e.pool, []orders.CartLine{{SKU: sku, Qty: 2}}, 0)
	if err != nil || q.Lines[0].Qty != 1 || q.Lines[0].Problem == "" {
		t.Fatalf("quote should clamp to available stock: %+v %v", q, err)
	}
}

func TestStatusFlowAdjustsInventory(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	sku := e.product(t, "FLOW-1", 1000, 1000, 10)

	cancelled, _ := e.orders.PlaceOrder(ctx, e.checkout("key-flow-cancel-001", orders.CartLine{SKU: sku, Qty: 4}))
	if err := e.orders.UpdateStatus(ctx, e.adminID, cancelled.ID, orders.StatusCancelled, "customer request"); err != nil {
		t.Fatal(err)
	}
	if st, res := e.stock(t, sku); st != 10 || res != 0 {
		t.Fatalf("after cancel stock=%d reserved=%d, want 10/0", st, res)
	}
	if err := e.orders.UpdateStatus(ctx, e.adminID, cancelled.ID, orders.StatusConfirmed, ""); !errors.Is(err, orders.ErrInvalidTransition) {
		t.Fatalf("cancelled order must not be reopened, got %v", err)
	}

	o, _ := e.orders.PlaceOrder(ctx, e.checkout("key-flow-ship-00001", orders.CartLine{SKU: sku, Qty: 3}))
	for _, s := range []string{orders.StatusConfirmed, orders.StatusProcessing, orders.StatusReadyToShip, orders.StatusShipped} {
		if err := e.orders.UpdateStatus(ctx, e.adminID, o.ID, s, ""); err != nil {
			t.Fatalf("to %s: %v", s, err)
		}
	}
	if st, res := e.stock(t, sku); st != 7 || res != 0 {
		t.Fatalf("after ship stock=%d reserved=%d, want 7/0", st, res)
	}
	if err := e.orders.UpdateStatus(ctx, e.adminID, o.ID, orders.StatusReturned, "damaged box"); err != nil {
		t.Fatal(err)
	}
	if st, res := e.stock(t, sku); st != 10 || res != 0 {
		t.Fatalf("after return stock=%d reserved=%d, want 10/0", st, res)
	}
	full, _ := e.orders.OrderByID(ctx, o.ID)
	if len(full.History) != 6 || full.PaymentStatus != "refunded" {
		t.Fatalf("history=%d payment=%s", len(full.History), full.PaymentStatus)
	}
}

func TestTrackRequiresMatchingPhone(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	sku := e.product(t, "TRK-1", 1000, 1000, 5)
	o, err := e.orders.PlaceOrder(ctx, e.checkout("key-track-00000001", orders.CartLine{SKU: sku, Qty: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.orders.Track(ctx, o.Number, "01712345678"); err != nil {
		t.Fatalf("matching phone: %v", err)
	}
	if _, err := e.orders.Track(ctx, o.Number, "01812345678"); !errors.Is(err, orders.ErrNotFound) {
		t.Fatalf("wrong phone must not reveal the order, got %v", err)
	}
}

func TestOffersApplyBestSingleDiscount(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `TRUNCATE offer_products, offer_categories, offers RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	cheap := e.product(t, "OFF-CHEAP", 100000, 60000, 5) // own price ৳600 beats a 20% campaign (৳800)
	full := e.product(t, "OFF-FULL", 100000, 100000, 5)  // campaign applies: ৳800
	var cheapID, fullID int64
	_ = e.pool.QueryRow(ctx, `SELECT id FROM products WHERE sku = 'OFF-CHEAP'`).Scan(&cheapID)
	_ = e.pool.QueryRow(ctx, `SELECT id FROM products WHERE sku = 'OFF-FULL'`).Scan(&fullID)

	mk := func(title, typ string, value int64, start, end string, ids ...int64) {
		var id int64
		err := e.pool.QueryRow(ctx, `INSERT INTO offers (title, slug, discount_type, discount_value, starts_at, ends_at)
			VALUES ($1, $1, $2, $3, now() + $4::interval, now() + $5::interval) RETURNING id`, title, typ, value, start, end).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		for _, pid := range ids {
			_, _ = e.pool.Exec(ctx, `INSERT INTO offer_products VALUES ($1, $2)`, id, pid)
		}
	}
	mk("twenty", "percent", 20, "-1 day", "1 day", cheapID, fullID)
	mk("expired-huge", "percent", 90, "-3 days", "-1 day", cheapID, fullID) // ended: ignored
	mk("future-huge", "fixed", 90000, "1 day", "2 days", cheapID, fullID)   // not started: ignored

	q, err := e.orders.Quote(ctx, e.pool, []orders.CartLine{{SKU: cheap, Qty: 1}, {SKU: full, Qty: 1}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if q.Lines[0].UnitPrice != 60000 || q.Lines[0].OfferTitle != "" {
		t.Fatalf("own lower price must win without stacking: %+v", q.Lines[0])
	}
	if q.Lines[1].UnitPrice != 80000 || q.Lines[1].OfferTitle != "twenty" {
		t.Fatalf("campaign price expected: %+v", q.Lines[1])
	}
	if q.Discount != 60000 {
		t.Fatalf("discount = %d, want 60000", q.Discount)
	}

	o, err := e.orders.PlaceOrder(ctx, e.checkout("key-offers-0000001", orders.CartLine{SKU: full, Qty: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if o.Items[0].UnitPrice != 80000 || o.Items[0].OfferTitle != "twenty" || o.Total != 160000+o.DeliveryCharge {
		t.Fatalf("order item %+v total %d", o.Items[0], o.Total)
	}
	page, err := e.catalog.ListProducts(ctx, catalog.ListParams{OnSale: true, MaxPrice: 85000})
	if err != nil || page.Total != 2 {
		t.Fatalf("price filters must use the effective price: total=%d err=%v", page.Total, err)
	}
}
