// Package steadfast is a minimal client for the Steadfast Courier merchant API (v1).
//
// Only endpoints and fields present in the official Postman documentation
// (https://documenter.getpostman.com/view/26211192/2sAYJ1mhpk) are used; see docs/steadfast.md.
// Authentication is the "Api-Key" and "Secret-Key" headers.
package steadfast

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultBaseURL = "https://portal.packzy.com/api/v1"

// Result is the raw exchange, kept so every call can be stored for auditing.
type Result struct {
	HTTPStatus int
	Body       string
}

// Error is returned for non-success responses. Ambiguous reports whether the request may
// have been processed by Steadfast even though we got no usable answer (timeouts, 5xx).
type Error struct {
	HTTPStatus int
	Message    string
	Ambiguous  bool
}

func (e *Error) Error() string {
	if e.HTTPStatus > 0 {
		return fmt.Sprintf("steadfast: HTTP %d: %s", e.HTTPStatus, e.Message)
	}
	return "steadfast: " + e.Message
}

type Client struct {
	baseURL   string
	apiKey    string
	secretKey string
	http      *http.Client
	// backoff between retries of read-only calls; overridable in tests.
	backoff []time.Duration
}

func New(baseURL, apiKey, secretKey string, timeout time.Duration) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, secretKey: secretKey,
		http:    &http.Client{Timeout: timeout},
		backoff: []time.Duration{500 * time.Millisecond, 2 * time.Second},
	}
}

// CreateOrderRequest mirrors the documented create_order body.
type CreateOrderRequest struct {
	Invoice          string      `json:"invoice"`
	RecipientName    string      `json:"recipient_name"`
	RecipientPhone   string      `json:"recipient_phone"`
	AlternativePhone string      `json:"alternative_phone,omitempty"`
	RecipientEmail   string      `json:"recipient_email,omitempty"`
	RecipientAddress string      `json:"recipient_address"`
	CODAmount        json.Number `json:"cod_amount"`
	ItemDescription  string      `json:"item_description,omitempty"`
	Note             string      `json:"note,omitempty"`
}

type Consignment struct {
	ConsignmentID int64  `json:"consignment_id"`
	Invoice       string `json:"invoice"`
	TrackingCode  string `json:"tracking_code"`
	Status        string `json:"status"`
}

// CreateOrder is NOT retried automatically: a lost response could otherwise create a second
// parcel. Callers recover ambiguous failures with TrackingsByInvoice.
func (c *Client) CreateOrder(ctx context.Context, req CreateOrderRequest) (*Consignment, Result, error) {
	var out struct {
		Status      int          `json:"status"`
		Message     string       `json:"message"`
		Consignment *Consignment `json:"consignment"`
	}
	res, err := c.do(ctx, http.MethodPost, "/create_order", req, &out, false)
	if err != nil {
		return nil, res, err
	}
	if out.Status != http.StatusOK || out.Consignment == nil || out.Consignment.ConsignmentID == 0 {
		msg := out.Message
		if msg == "" {
			msg = "unexpected response: " + truncate(res.Body, 300)
		}
		return nil, res, &Error{HTTPStatus: res.HTTPStatus, Message: msg}
	}
	return out.Consignment, res, nil
}

// StatusByCID returns the current delivery status of a consignment (e.g. "in_review", "delivered").
func (c *Client) StatusByCID(ctx context.Context, cid int64) (string, Result, error) {
	var out struct {
		Status         int    `json:"status"`
		DeliveryStatus string `json:"delivery_status"`
	}
	res, err := c.do(ctx, http.MethodGet, "/status_by_cid/"+strconv.FormatInt(cid, 10), nil, &out, true)
	if err != nil {
		return "", res, err
	}
	if out.DeliveryStatus == "" {
		return "", res, &Error{HTTPStatus: res.HTTPStatus, Message: "no delivery_status in response"}
	}
	return out.DeliveryStatus, res, nil
}

// TrackingsByInvoice returns the consignment ID registered for our invoice, or 0 if none.
func (c *Client) TrackingsByInvoice(ctx context.Context, invoice string) (int64, Result, error) {
	var out struct {
		Status   int `json:"status"`
		Tracking []struct {
			ConsignmentID int64 `json:"consignment_id"`
		} `json:"tracking"`
	}
	res, err := c.do(ctx, http.MethodGet, "/trackings_by_invoice/"+url.PathEscape(invoice), nil, &out, true)
	if err != nil {
		return 0, res, err
	}
	for _, t := range out.Tracking {
		if t.ConsignmentID != 0 {
			return t.ConsignmentID, res, nil
		}
	}
	return 0, res, nil
}

func (c *Client) Balance(ctx context.Context) (float64, error) {
	var out struct {
		Status         int     `json:"status"`
		CurrentBalance float64 `json:"current_balance"`
	}
	_, err := c.do(ctx, http.MethodGet, "/get_balance", nil, &out, true)
	return out.CurrentBalance, err
}

// FraudCheck reports a phone number's parcel history across Steadfast merchants.
type FraudCheck struct {
	TotalParcels   int `json:"total_parcels"`
	TotalDelivered int `json:"total_delivered"`
	TotalCancelled int `json:"total_cancelled"`
}

func (f FraudCheck) SuccessRate() int {
	if f.TotalParcels == 0 {
		return 0
	}
	return f.TotalDelivered * 100 / f.TotalParcels
}

func (c *Client) FraudCheck(ctx context.Context, phone string) (*FraudCheck, error) {
	var out FraudCheck
	if _, err := c.do(ctx, http.MethodGet, "/fraud_check/"+url.PathEscape(phone), nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// do performs a request. Read-only calls (retry=true) are retried on network errors,
// 429 and 5xx responses with backoff.
func (c *Client) do(ctx context.Context, method, path string, body, out any, retry bool) (Result, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return Result{}, err
		}
	}
	attempts := 1
	if retry {
		attempts += len(c.backoff)
	}
	var res Result
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(c.backoff[i-1]):
			}
		}
		res, lastErr = c.once(ctx, method, path, payload)
		var se *Error
		if lastErr == nil || !errors.As(lastErr, &se) || !se.Ambiguous {
			break
		}
	}
	if lastErr != nil {
		return res, lastErr
	}
	if out != nil {
		if err := json.Unmarshal([]byte(res.Body), out); err != nil {
			return res, &Error{HTTPStatus: res.HTTPStatus, Message: "invalid JSON response: " + truncate(res.Body, 200)}
		}
	}
	return res, nil
}

func (c *Client) once(ctx context.Context, method, path string, payload []byte) (Result, error) {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Api-Key", c.apiKey)
	req.Header.Set("Secret-Key", c.secretKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// A request that never connected cannot have been processed.
		var opErr *net.OpError
		notSent := errors.As(err, &opErr) && opErr.Op == "dial"
		return Result{}, &Error{Message: err.Error(), Ambiguous: !notSent}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	res := Result{HTTPStatus: resp.StatusCode, Body: string(b)}
	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return res, &Error{HTTPStatus: resp.StatusCode, Message: truncate(res.Body, 300), Ambiguous: true}
	case resp.StatusCode >= 400:
		return res, &Error{HTTPStatus: resp.StatusCode, Message: errorMessage(b)}
	}
	return res, nil
}

// errorMessage extracts "message" and validation "errors" from an error body when present.
func errorMessage(body []byte) string {
	var e struct {
		Message string              `json:"message"`
		Errors  map[string][]string `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil || (e.Message == "" && len(e.Errors) == 0) {
		return truncate(string(body), 300)
	}
	var msg strings.Builder
	msg.WriteString(e.Message)
	for field, list := range e.Errors {
		fmt.Fprintf(&msg, " [%s: %s]", field, strings.Join(list, ", "))
	}
	return msg.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
