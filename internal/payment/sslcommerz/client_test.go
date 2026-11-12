package sslcommerz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreateSession(t *testing.T) {
	var got map[string]string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gwprocess/v4/api.php" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = r.ParseForm()
		got = map[string]string{}
		for k := range r.PostForm {
			got[k] = r.PostForm.Get(k)
		}
		_, _ = w.Write([]byte(`{"status":"SUCCESS","sessionkey":"abc","GatewayPageURL":"` + srv.URL + `/EasyCheckOut/abc"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "store1", "secret", 5*time.Second)
	s, _, err := c.CreateSession(context.Background(), SessionRequest{
		TranID: "261106-12345-aa", Amount: "1250.50", SuccessURL: "https://shop/s", CustName: "Rahim", NumItems: 2,
		ProductName: strings.Repeat("x", 400),
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.GatewayURL != srv.URL+"/EasyCheckOut/abc" || s.SessionKey != "abc" {
		t.Fatalf("session = %+v", s)
	}
	for k, want := range map[string]string{
		"store_id": "store1", "store_passwd": "secret", "total_amount": "1250.50", "currency": "BDT",
		"tran_id": "261106-12345-aa", "num_of_item": "2", "cus_email": "customer@example.com",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	if n := len([]rune(got["product_name"])); n > 250 {
		t.Errorf("product_name not truncated: %d", n)
	}
}

func TestCreateSessionRejections(t *testing.T) {
	for name, body := range map[string]string{
		"failed":        `{"status":"FAILED","failedreason":"Store Credential Error"}`,
		"foreign host":  `{"status":"SUCCESS","GatewayPageURL":"https://evil.example/pay"}`,
		"not json":      `<html>`,
		"missing gwurl": `{"status":"SUCCESS"}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			if _, _, err := New(srv.URL, "s", "p", time.Second).CreateSession(context.Background(), SessionRequest{TranID: "t"}); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestValidate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/validator/api/validationserverAPI.php" || q.Get("val_id") != "V1" || q.Get("store_id") != "s" || q.Get("format") != "json" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"status":"VALID","tran_id":"T1","val_id":"V1","amount":"100.00","currency":"BDT","card_type":"BKASH-BKash","risk_level":"0"}`))
	}))
	defer srv.Close()
	v, _, err := New(srv.URL, "s", "p", time.Second).Validate(context.Background(), "V1")
	if err != nil {
		t.Fatal(err)
	}
	if !v.OK() || v.TranID != "T1" || v.Amount != "100.00" || v.CardType != "BKASH-BKash" {
		t.Fatalf("validation = %+v", v)
	}
	if (&Validation{Status: "INVALID_TRANSACTION"}).OK() {
		t.Fatal("invalid transaction reported as OK")
	}
}
