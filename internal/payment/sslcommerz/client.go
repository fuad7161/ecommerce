// Package sslcommerz is a minimal client for the SSLCommerz hosted payment gateway (API v4).
//
// Flow: CreateSession returns a GatewayPageURL the customer is redirected to. SSLCommerz then
// POSTs the customer back to our success/fail/cancel URLs and sends a server-to-server IPN.
// Neither callback is trusted on its own: a payment only counts after Validate confirms the
// val_id with SSLCommerz's Order Validation API.
//
// Docs: https://developer.sslcommerz.com/doc/v4/
package sslcommerz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	SandboxBaseURL = "https://sandbox.sslcommerz.com"
	LiveBaseURL    = "https://securepay.sslcommerz.com"
)

type Client struct {
	baseURL   string
	storeID   string
	storePass string
	http      *http.Client
}

func New(baseURL, storeID, storePass string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), storeID: storeID, storePass: storePass,
		http: &http.Client{Timeout: timeout},
	}
}

// BaseURL is the gateway origin; the storefront CSP must allow redirecting forms to it.
func (c *Client) BaseURL() string { return c.baseURL }

// SessionRequest holds the order-specific fields of a session request. Amount is in taka
// as a decimal string ("1250.50").
type SessionRequest struct {
	TranID      string
	Amount      string
	SuccessURL  string
	FailURL     string
	CancelURL   string
	IPNURL      string
	CustName    string
	CustPhone   string
	CustEmail   string
	CustAddress string
	CustCity    string
	NumItems    int
	ProductName string
}

type Session struct {
	GatewayURL string
	SessionKey string
}

// CreateSession starts a hosted payment session.
func (c *Client) CreateSession(ctx context.Context, in SessionRequest) (*Session, []byte, error) {
	email := in.CustEmail
	if email == "" {
		// SSLCommerz requires an email; guest checkout does not collect one.
		email = "customer@example.com"
	}
	form := url.Values{
		"store_id":         {c.storeID},
		"store_passwd":     {c.storePass},
		"total_amount":     {in.Amount},
		"currency":         {"BDT"},
		"tran_id":          {in.TranID},
		"success_url":      {in.SuccessURL},
		"fail_url":         {in.FailURL},
		"cancel_url":       {in.CancelURL},
		"ipn_url":          {in.IPNURL},
		"cus_name":         {in.CustName},
		"cus_email":        {email},
		"cus_add1":         {in.CustAddress},
		"cus_city":         {in.CustCity},
		"cus_country":      {"Bangladesh"},
		"cus_phone":        {in.CustPhone},
		"shipping_method":  {"Courier"},
		"num_of_item":      {fmt.Sprint(max(in.NumItems, 1))},
		"ship_name":        {in.CustName},
		"ship_add1":        {in.CustAddress},
		"ship_city":        {in.CustCity},
		"ship_postcode":    {"1000"},
		"ship_country":     {"Bangladesh"},
		"product_name":     {truncate(in.ProductName, 250)},
		"product_category": {"General"},
		"product_profile":  {"physical-goods"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/gwprocess/v4/api.php", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := c.do(req)
	if err != nil {
		return nil, body, err
	}
	var res struct {
		Status         string `json:"status"`
		FailedReason   string `json:"failedreason"`
		SessionKey     string `json:"sessionkey"`
		GatewayPageURL string `json:"GatewayPageURL"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, body, fmt.Errorf("sslcommerz: decode session response: %w", err)
	}
	if !strings.EqualFold(res.Status, "SUCCESS") || res.GatewayPageURL == "" {
		return nil, body, fmt.Errorf("sslcommerz: session rejected: %s", or(res.FailedReason, res.Status))
	}
	if !strings.HasPrefix(res.GatewayPageURL, c.baseURL+"/") {
		return nil, body, errors.New("sslcommerz: unexpected gateway URL " + res.GatewayPageURL)
	}
	return &Session{GatewayURL: res.GatewayPageURL, SessionKey: res.SessionKey}, body, nil
}

// Validation is the result of the Order Validation API.
type Validation struct {
	Status     string `json:"status"` // VALID, VALIDATED (already validated before), INVALID_TRANSACTION
	TranID     string `json:"tran_id"`
	ValID      string `json:"val_id"`
	Amount     string `json:"amount"`
	Currency   string `json:"currency"`
	BankTranID string `json:"bank_tran_id"`
	CardType   string `json:"card_type"`
	RiskLevel  string `json:"risk_level"`
	RiskTitle  string `json:"risk_title"`
}

// OK reports whether SSLCommerz confirms the transaction as successfully paid.
func (v *Validation) OK() bool { return v.Status == "VALID" || v.Status == "VALIDATED" }

// Validate asks SSLCommerz whether val_id is a genuine, successful payment.
func (c *Client) Validate(ctx context.Context, valID string) (*Validation, []byte, error) {
	q := url.Values{"val_id": {valID}, "store_id": {c.storeID}, "store_passwd": {c.storePass}, "v": {"1"}, "format": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/validator/api/validationserverAPI.php?"+q.Encode(), nil)
	if err != nil {
		return nil, nil, err
	}
	body, err := c.do(req)
	if err != nil {
		return nil, body, err
	}
	var v Validation
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, body, fmt.Errorf("sslcommerz: decode validation response: %w", err)
	}
	return &v, body, nil
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sslcommerz: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("sslcommerz: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("sslcommerz: HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-3]) + "..."
	}
	return s
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
