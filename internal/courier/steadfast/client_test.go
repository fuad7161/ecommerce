package steadfast

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(url string) *Client {
	c := New(url, "key", "secret", 200*time.Millisecond)
	c.backoff = []time.Duration{time.Millisecond, time.Millisecond}
	return c
}

func TestCreateOrderSendsDocumentedRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/create_order" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Api-Key") != "key" || r.Header.Get("Secret-Key") != "secret" {
			t.Error("missing auth headers")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["invoice"] != "261030-00001" || body["cod_amount"] != 1250.5 || body["recipient_phone"] != "01712345678" {
			t.Errorf("unexpected body %v", body)
		}
		_, _ = w.Write([]byte(`{"status":200,"message":"Consignment has been created successfully.",
			"consignment":{"consignment_id":131822154,"invoice":"261030-00001","tracking_code":"7DB724A65D4","status":"in_review"}}`))
	}))
	defer srv.Close()

	c, _, err := newTestClient(srv.URL).CreateOrder(context.Background(), CreateOrderRequest{
		Invoice: "261030-00001", RecipientName: "Rahim", RecipientPhone: "01712345678",
		RecipientAddress: "House 1, Mirpur, Dhaka", CODAmount: json.Number("1250.50"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.ConsignmentID != 131822154 || c.TrackingCode != "7DB724A65D4" || c.Status != "in_review" {
		t.Fatalf("got %+v", c)
	}
}

func TestCreateOrderValidationErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"The given data was invalid.","errors":{"invoice":["The invoice has already been taken."]}}`))
	}))
	defer srv.Close()

	_, res, err := newTestClient(srv.URL).CreateOrder(context.Background(), CreateOrderRequest{Invoice: "X"})
	var se *Error
	if !errors.As(err, &se) || se.Ambiguous || se.HTTPStatus != 422 || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	if res.HTTPStatus != 422 || se.Message != "The given data was invalid. [invoice: The invoice has already been taken.]" {
		t.Fatalf("message %q", se.Message)
	}
}

func TestCreateOrderServerErrorIsAmbiguousAndNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	_, _, err := newTestClient(srv.URL).CreateOrder(context.Background(), CreateOrderRequest{Invoice: "X"})
	var se *Error
	if !errors.As(err, &se) || !se.Ambiguous || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestCreateOrderTimeoutIsAmbiguous(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()
	_, _, err := newTestClient(srv.URL).CreateOrder(context.Background(), CreateOrderRequest{Invoice: "X"})
	var se *Error
	if !errors.As(err, &se) || !se.Ambiguous {
		t.Fatalf("timeout should be ambiguous, got %v", err)
	}
}

func TestConnectionRefusedIsNotAmbiguous(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, _, err := newTestClient(url).CreateOrder(context.Background(), CreateOrderRequest{Invoice: "X"})
	var se *Error
	if !errors.As(err, &se) || se.Ambiguous {
		t.Fatalf("connection refused means not sent, got %v", err)
	}
}

func TestStatusByCIDRetriesTransientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/status_by_cid/42" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":200,"delivery_status":"delivered"}`))
	}))
	defer srv.Close()
	st, _, err := newTestClient(srv.URL).StatusByCID(context.Background(), 42)
	if err != nil || st != "delivered" || calls.Load() != 3 {
		t.Fatalf("status=%q err=%v calls=%d", st, err, calls.Load())
	}
}

func TestTrackingsByInvoice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/trackings_by_invoice/FOUND" {
			_, _ = w.Write([]byte(`{"status":200,"tracking":[{"id":1,"consignment_id":170577767,"text":"Consignment created"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":200,"tracking":[]}`))
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)
	if cid, _, err := c.TrackingsByInvoice(context.Background(), "FOUND"); err != nil || cid != 170577767 {
		t.Fatalf("cid=%d err=%v", cid, err)
	}
	if cid, _, err := c.TrackingsByInvoice(context.Background(), "NONE"); err != nil || cid != 0 {
		t.Fatalf("cid=%d err=%v", cid, err)
	}
}

func TestFraudCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"total_parcels":24,"total_delivered":9,"total_cancelled":15,"total_fraud_reports":[]}`))
	}))
	defer srv.Close()
	f, err := newTestClient(srv.URL).FraudCheck(context.Background(), "01712345678")
	if err != nil || f.TotalParcels != 24 || f.SuccessRate() != 37 {
		t.Fatalf("%+v %v", f, err)
	}
}
