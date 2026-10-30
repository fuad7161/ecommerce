package courier_test

// Integration tests: real PostgreSQL (TEST_DATABASE_URL) + a fake Steadfast server.

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuad71/ecommerce/internal/auth"
	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/courier"
	"github.com/fuad71/ecommerce/internal/courier/steadfast"
	"github.com/fuad71/ecommerce/internal/db"
	"github.com/fuad71/ecommerce/internal/orders"
	"github.com/fuad71/ecommerce/internal/settings"
)

// fake simulates the documented Steadfast endpoints.
type fake struct {
	creates    atomic.Int32
	createCode atomic.Int32 // HTTP status to return from create_order; 0 = success
	registered sync.Map     // invoice -> consignment id (what Steadfast "knows")
	status     atomic.Value // delivery status returned by status_by_cid
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/create_order":
		n := f.creates.Add(1)
		time.Sleep(20 * time.Millisecond)
		var body struct{ Invoice string }
		_ = decodeJSON(r, &body)
		cid := int64(1000 + n)
		switch code := int(f.createCode.Load()); code {
		case 0:
			f.registered.Store(body.Invoice, cid)
			fmt.Fprintf(w, `{"status":200,"message":"ok","consignment":{"consignment_id":%d,"invoice":%q,"tracking_code":"TRK%d","status":"in_review"}}`, cid, body.Invoice, cid)
		case http.StatusBadGateway:
			f.registered.Store(body.Invoice, cid) // processed, but the response was lost
			w.WriteHeader(code)
		default:
			w.WriteHeader(code)
			fmt.Fprint(w, `{"message":"The given data was invalid.","errors":{"recipient_phone":["invalid"]}}`)
		}
	case strings.HasPrefix(r.URL.Path, "/trackings_by_invoice/"):
		inv := strings.TrimPrefix(r.URL.Path, "/trackings_by_invoice/")
		if cid, ok := f.registered.Load(inv); ok {
			fmt.Fprintf(w, `{"status":200,"tracking":[{"id":1,"consignment_id":%d}]}`, cid)
			return
		}
		fmt.Fprint(w, `{"status":200,"tracking":[]}`)
	case strings.HasPrefix(r.URL.Path, "/status_by_cid/"):
		fmt.Fprintf(w, `{"status":200,"delivery_status":%q}`, f.status.Load())
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type env struct {
	pool    *pgxpool.Pool
	orders  *orders.Service
	courier *courier.Service
	fake    *fake
	adminID int64
	sku     string
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
	if _, err := pool.Exec(ctx, `TRUNCATE courier_events, courier_consignments, inventory_movements, order_notes,
		order_status_history, order_items, orders, customers, offer_products, offer_categories, offers, product_images,
		product_variants, products, categories, audit_logs, admins RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	a, err := auth.NewService(pool).Create(ctx, "c@example.com", "Courier Tester", "test-password-123", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	in := &catalog.ProductInput{Name: "Courier product", SKU: "CR-1", RegularPrice: 50000, SellingPrice: 50000,
		Status: catalog.StatusActive, InitialStock: 100}
	if err := catalog.NewService(pool).SaveProduct(ctx, a.ID, in); err != nil {
		t.Fatal(err)
	}
	f := &fake{}
	f.status.Store("in_review")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	st := settings.NewService(pool)
	ord := orders.NewService(pool, st)
	client := steadfast.New(srv.URL, "k", "s", 2*time.Second)
	return &env{pool: pool, orders: ord, courier: courier.New(pool, ord, client, false), fake: f, adminID: a.ID, sku: in.SKU}
}

var keySeq atomic.Int32

func (e *env) order(t *testing.T, status ...string) *orders.Order {
	t.Helper()
	ctx := context.Background()
	zones, _ := settings.NewService(e.pool).Zones(ctx, true)
	o, err := e.orders.PlaceOrder(ctx, orders.CheckoutInput{
		Name: "Karim", Phone: "01812345678", District: "Dhaka", Area: "Gulshan", Address: "Road 11, House 4, Gulshan 1",
		ZoneID: zones[0].ID, PaymentMethod: "cod", Confirmed: true,
		IdempotencyKey: fmt.Sprintf("courier-test-key-%06d", keySeq.Add(1)), Items: []orders.CartLine{{SKU: e.sku, Qty: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range status {
		if err := e.orders.UpdateStatus(ctx, e.adminID, o.ID, s, ""); err != nil {
			t.Fatal(err)
		}
	}
	return o
}

func TestSendRequiresConfirmation(t *testing.T) {
	e := setup(t)
	o := e.order(t) // still pending
	if _, err := e.courier.Send(context.Background(), e.adminID, o.ID, ""); !errors.Is(err, courier.ErrNotEligible) {
		t.Fatalf("want ErrNotEligible, got %v", err)
	}
	if e.fake.creates.Load() != 0 {
		t.Fatal("API must not be called for unconfirmed orders")
	}
}

func TestSendCreatesConsignmentOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	o := e.order(t, orders.StatusConfirmed)

	var wg sync.WaitGroup
	var ok, dup atomic.Int32
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.courier.Send(ctx, e.adminID, o.ID, "fragile")
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, courier.ErrAlreadySent):
				dup.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || dup.Load() != 4 || e.fake.creates.Load() != 1 {
		t.Fatalf("ok=%d dup=%d api calls=%d", ok.Load(), dup.Load(), e.fake.creates.Load())
	}
	c, _ := e.courier.Consignment(ctx, o.ID)
	if c.State != "created" || c.ConsignmentID == nil || c.TrackingCode == "" || c.DeliveryStatus != "in_review" || c.CODAmount != o.Total {
		t.Fatalf("consignment %+v", c)
	}
	if _, err := e.courier.Send(ctx, e.adminID, o.ID, ""); !errors.Is(err, courier.ErrAlreadySent) {
		t.Fatalf("second send must be rejected, got %v", err)
	}
}

func TestFailedSendCanBeRetried(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	o := e.order(t, orders.StatusConfirmed)

	e.fake.createCode.Store(http.StatusUnprocessableEntity)
	if _, err := e.courier.Send(ctx, e.adminID, o.ID, ""); err == nil || !strings.Contains(err.Error(), "recipient_phone") {
		t.Fatalf("expected validation error, got %v", err)
	}
	c, _ := e.courier.Consignment(ctx, o.ID)
	if c.State != "failed" || c.LastError == "" {
		t.Fatalf("consignment %+v", c)
	}

	e.fake.createCode.Store(0)
	if c, err := e.courier.Send(ctx, e.adminID, o.ID, ""); err != nil || c.State != "created" {
		t.Fatalf("retry: %+v %v", c, err)
	}
	events, _ := e.courier.Events(ctx, o.ID)
	if len(events) != 2 || events[0].Kind != "create" || events[1].Kind != "create_failed" || events[1].HTTPStatus != 422 {
		t.Fatalf("events %+v", events)
	}
}

func TestAmbiguousFailureIsReconciled(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	o := e.order(t, orders.StatusConfirmed)

	e.fake.createCode.Store(http.StatusBadGateway) // Steadfast created it but we got a 502
	c, err := e.courier.Send(ctx, e.adminID, o.ID, "")
	if err != nil || c.State != "created" || c.ConsignmentID == nil {
		t.Fatalf("expected recovery via trackings_by_invoice: %+v %v", c, err)
	}
	if e.fake.creates.Load() != 1 {
		t.Fatalf("must not create a second parcel, calls=%d", e.fake.creates.Load())
	}
}

func TestSyncDeliveredCompletesOrder(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	o := e.order(t, orders.StatusConfirmed, orders.StatusProcessing, orders.StatusReadyToShip)
	if _, err := e.courier.Send(ctx, e.adminID, o.ID, ""); err != nil {
		t.Fatal(err)
	}

	e.fake.status.Store("hold")
	if _, err := e.courier.Sync(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.orders.OrderByID(ctx, o.ID); got.Status != orders.StatusReadyToShip {
		t.Fatalf("hold must not change the order, got %s", got.Status)
	}

	e.fake.status.Store("delivered")
	if n, err := e.courier.SyncDue(ctx, 0, 10); err != nil || n != 1 {
		t.Fatalf("SyncDue n=%d err=%v", n, err)
	}
	got, _ := e.orders.OrderByID(ctx, o.ID)
	if got.Status != orders.StatusDelivered || got.PaymentStatus != "paid" {
		t.Fatalf("order %s/%s", got.Status, got.PaymentStatus)
	}
	var stock, reserved int
	_ = e.pool.QueryRow(ctx, `SELECT stock, reserved FROM product_variants WHERE sku = $1`, e.sku).Scan(&stock, &reserved)
	if stock != 99 || reserved != 0 {
		t.Fatalf("stock=%d reserved=%d, want 99/0", stock, reserved)
	}
	if n, _ := e.courier.SyncDue(ctx, 0, 10); n != 0 {
		t.Fatal("delivered consignments must not be polled again")
	}
}

func TestBuildRequest(t *testing.T) {
	o := &orders.Order{Number: "261030-00001", CustomerName: "Rahim", Phone: "01712345678", Address: "House 1",
		Area: "Mirpur", District: "Dhaka", Total: 125050, DeliveryNotes: "Call first",
		Items: []orders.Item{{ProductName: "Tee", VariantName: "XL", Quantity: 2}}}
	r := courier.BuildRequest(o, "fragile")
	if r.Invoice != o.Number || r.CODAmount != "1250.50" || r.RecipientAddress != "House 1, Mirpur, Dhaka" ||
		r.ItemDescription != "Tee (XL) x2" || r.Note != "Call first | fragile" {
		t.Fatalf("%+v", r)
	}
}

func decodeJSON(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }
