// Package courier sends confirmed orders to Steadfast Courier and keeps delivery status in sync.
//
// Flow: the admin confirms an order, then clicks "Send to Steadfast" (or, if AutoSend is
// configured, confirming does it). A row in courier_consignments guards against duplicates:
// it is inserted in state "creating" before the API call, and the unique order_id
// constraint makes a second concurrent attempt fail fast. Ambiguous failures (timeouts, 5xx)
// are reconciled through trackings_by_invoice before the order is marked failed.
package courier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuad71/ecommerce/internal/courier/steadfast"
	"github.com/fuad71/ecommerce/internal/db"
	"github.com/fuad71/ecommerce/internal/money"
	"github.com/fuad71/ecommerce/internal/orders"
)

var (
	ErrNotConfigured = errors.New("Steadfast API credentials are not configured")
	ErrNotEligible   = errors.New("only confirmed, processing or ready-to-ship orders can be sent to the courier")
	ErrAlreadySent   = errors.New("this order already has a courier consignment (or one is being created)")
	ErrNoConsignment = errors.New("this order has no courier consignment")
)

// Eligible order statuses for creating a consignment.
var eligible = []string{orders.StatusConfirmed, orders.StatusProcessing, orders.StatusReadyToShip}

// Final courier statuses are not polled any more.
var finalStatuses = []string{"delivered", "partial_delivered", "cancelled"}

type Consignment struct {
	OrderID        int64
	State          string
	ConsignmentID  *int64
	TrackingCode   string
	Invoice        string
	CODAmount      int64
	DeliveryStatus string
	LastError      string
	LastSyncedAt   *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// StatusLabel turns a Steadfast status like "partial_delivered" into "Partial delivered".
func (c *Consignment) StatusLabel() string { return Label(c.DeliveryStatus) }

func Label(s string) string {
	if s == "" {
		return "—"
	}
	s = strings.ReplaceAll(s, "_", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

type Event struct {
	Kind       string
	Status     string
	HTTPStatus int
	Response   string
	Error      string
	CreatedAt  time.Time
}

type Service struct {
	pool     *pgxpool.Pool
	orders   *orders.Service
	client   *steadfast.Client
	AutoSend bool
}

// New returns a service; client may be nil when credentials are not configured.
func New(pool *pgxpool.Pool, ord *orders.Service, client *steadfast.Client, autoSend bool) *Service {
	return &Service{pool: pool, orders: ord, client: client, AutoSend: autoSend && client != nil}
}

func (s *Service) Configured() bool { return s.client != nil }

func (s *Service) Consignment(ctx context.Context, orderID int64) (*Consignment, error) {
	var c Consignment
	err := s.pool.QueryRow(ctx, `SELECT order_id, state, consignment_id, tracking_code, invoice, cod_amount,
			delivery_status, last_error, last_synced_at, created_at, updated_at
		FROM courier_consignments WHERE order_id = $1`, orderID).
		Scan(&c.OrderID, &c.State, &c.ConsignmentID, &c.TrackingCode, &c.Invoice, &c.CODAmount,
			&c.DeliveryStatus, &c.LastError, &c.LastSyncedAt, &c.CreatedAt, &c.UpdatedAt)
	if db.IsNoRows(err) {
		return nil, nil
	}
	return &c, err
}

func (s *Service) Events(ctx context.Context, orderID int64) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `SELECT kind, status, http_status, response, error, created_at
		FROM courier_events WHERE order_id = $1 ORDER BY created_at DESC, id DESC LIMIT 30`, orderID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		err := r.Scan(&e.Kind, &e.Status, &e.HTTPStatus, &e.Response, &e.Error, &e.CreatedAt)
		return e, err
	})
}

func (s *Service) event(ctx context.Context, orderID int64, kind, status string, req any, res steadfast.Result, callErr error) {
	var reqJSON []byte
	if req != nil {
		reqJSON, _ = json.Marshal(req)
	}
	errText := ""
	if callErr != nil {
		errText = callErr.Error()
	}
	if len(res.Body) > 8000 {
		res.Body = res.Body[:8000]
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO courier_events (order_id, kind, status, http_status, request, response, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, orderID, kind, status, res.HTTPStatus, reqJSON, res.Body, errText); err != nil {
		slog.ErrorContext(ctx, "courier event write failed", "order", orderID, "err", err)
	}
}

// BuildRequest maps an order to the documented create_order fields.
func BuildRequest(o *orders.Order, note string) steadfast.CreateOrderRequest {
	var items []string
	for _, it := range o.Items {
		name := it.ProductName
		if it.VariantName != "" {
			name += " (" + it.VariantName + ")"
		}
		items = append(items, fmt.Sprintf("%s x%d", name, it.Quantity))
	}
	desc := strings.Join(items, ", ")
	if r := []rune(desc); len(r) > 250 {
		desc = string(r[:247]) + "..."
	}
	notes := strings.TrimSpace(strings.Join(slices.DeleteFunc([]string{o.DeliveryNotes, strings.TrimSpace(note)},
		func(s string) bool { return s == "" }), " | "))
	address := fmt.Sprintf("%s, %s, %s", o.Address, o.Area, o.District)
	if r := []rune(address); len(r) > 250 {
		address = string(r[:250])
	}
	return steadfast.CreateOrderRequest{
		Invoice:          o.Number,
		RecipientName:    o.CustomerName,
		RecipientPhone:   o.Phone,
		RecipientAddress: address,
		CODAmount:        json.Number(money.Plain(o.Total)),
		ItemDescription:  desc,
		Note:             notes,
	}
}

// Send creates a Steadfast consignment for an order.
func (s *Service) Send(ctx context.Context, adminID, orderID int64, note string) (*Consignment, error) {
	if s.client == nil {
		return nil, ErrNotConfigured
	}
	o, err := s.orders.OrderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(eligible, o.Status) {
		return nil, ErrNotEligible
	}

	// Claim the order: insert, or retake a previously failed attempt. Anything else is a duplicate.
	var claimed bool
	err = s.pool.QueryRow(ctx, `INSERT INTO courier_consignments (order_id, state, invoice, cod_amount, created_by)
		VALUES ($1, 'creating', $2, $3, NULLIF($4, 0))
		ON CONFLICT (order_id) DO UPDATE SET state = 'creating', cod_amount = EXCLUDED.cod_amount,
			last_error = '', updated_at = now()
			WHERE courier_consignments.state = 'failed'
		RETURNING true`, o.ID, o.Number, o.Total, adminID).Scan(&claimed)
	if db.IsNoRows(err) {
		return nil, ErrAlreadySent
	}
	if err != nil {
		return nil, err
	}

	req := BuildRequest(o, note)
	cons, res, callErr := s.client.CreateOrder(ctx, req)
	if callErr == nil {
		s.event(ctx, o.ID, "create", cons.Status, req, res, nil)
		return s.markCreated(ctx, o.ID, cons.ConsignmentID, cons.TrackingCode, cons.Status)
	}
	s.event(ctx, o.ID, "create_failed", "", req, res, callErr)

	// The request may have reached Steadfast. Look the invoice up before allowing a retry.
	var se *steadfast.Error
	if errors.As(callErr, &se) && se.Ambiguous {
		cid, lres, lerr := s.client.TrackingsByInvoice(ctx, o.Number)
		s.event(ctx, o.ID, "reconcile", "", nil, lres, lerr)
		if lerr == nil && cid != 0 {
			status, sres, serr := s.client.StatusByCID(ctx, cid)
			s.event(ctx, o.ID, "status_sync", status, nil, sres, serr)
			return s.markCreated(ctx, o.ID, cid, "", status)
		}
	}
	if _, err := s.pool.Exec(context.WithoutCancel(ctx), `UPDATE courier_consignments SET state = 'failed', last_error = $2,
		updated_at = now() WHERE order_id = $1`, o.ID, callErr.Error()); err != nil {
		return nil, err
	}
	return nil, callErr
}

func (s *Service) markCreated(ctx context.Context, orderID, cid int64, tracking, status string) (*Consignment, error) {
	_, err := s.pool.Exec(context.WithoutCancel(ctx), `UPDATE courier_consignments SET state = 'created', consignment_id = $2,
			tracking_code = $3, delivery_status = $4, last_error = '', last_synced_at = now(), updated_at = now()
		WHERE order_id = $1`, orderID, cid, tracking, status)
	if err != nil {
		return nil, err
	}
	return s.Consignment(ctx, orderID)
}

// Sync refreshes one consignment's delivery status and applies it to the order.
func (s *Service) Sync(ctx context.Context, orderID int64) (*Consignment, error) {
	if s.client == nil {
		return nil, ErrNotConfigured
	}
	c, err := s.Consignment(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if c == nil || c.ConsignmentID == nil {
		return nil, ErrNoConsignment
	}
	status, res, callErr := s.client.StatusByCID(ctx, *c.ConsignmentID)
	s.event(ctx, orderID, "status_sync", status, nil, res, callErr)
	if callErr != nil {
		_, _ = s.pool.Exec(ctx, `UPDATE courier_consignments SET last_error = $2, last_synced_at = now() WHERE order_id = $1`,
			orderID, callErr.Error())
		return c, callErr
	}
	if _, err := s.pool.Exec(ctx, `UPDATE courier_consignments SET delivery_status = $2, last_error = '',
		last_synced_at = now(), updated_at = now() WHERE order_id = $1`, orderID, status); err != nil {
		return nil, err
	}
	if err := s.applyStatus(ctx, orderID, status); err != nil {
		slog.WarnContext(ctx, "apply courier status", "order", orderID, "status", status, "err", err)
	}
	return s.Consignment(ctx, orderID)
}

// applyStatus moves the order forward when the courier reports delivery. Other courier
// statuses (hold, cancelled, partial_delivered…) are shown to the admin, who decides
// what to do, because they may need a conversation with the customer or a return.
func (s *Service) applyStatus(ctx context.Context, orderID int64, courierStatus string) error {
	if courierStatus != "delivered" {
		return nil
	}
	o, err := s.orders.OrderByID(ctx, orderID)
	if err != nil {
		return err
	}
	var path []string
	switch o.Status {
	case orders.StatusReadyToShip:
		path = []string{orders.StatusShipped, orders.StatusDelivered}
	case orders.StatusShipped, orders.StatusInTransit:
		path = []string{orders.StatusDelivered}
	}
	for _, to := range path {
		if err := s.orders.UpdateStatus(ctx, 0, orderID, to, "Steadfast reported: delivered"); err != nil {
			return err
		}
	}
	return nil
}

// SyncDue refreshes consignments that are not final and were not synced within maxAge.
func (s *Service) SyncDue(ctx context.Context, maxAge time.Duration, limit int) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT order_id FROM courier_consignments
		WHERE state = 'created' AND NOT (delivery_status = ANY($1))
			AND (last_synced_at IS NULL OR last_synced_at < now() - $2::interval)
			AND created_at > now() - interval '60 days'
		ORDER BY last_synced_at NULLS FIRST LIMIT $3`, finalStatuses, fmt.Sprintf("%d seconds", int(maxAge.Seconds())), limit)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if _, err := s.Sync(ctx, id); err == nil {
			n++
		}
	}
	return n, nil
}

// RunSync polls Steadfast every interval until ctx is cancelled. A Postgres advisory lock
// ensures only one app instance syncs at a time.
func (s *Service) RunSync(ctx context.Context, interval time.Duration) {
	if s.client == nil || interval <= 0 {
		return
	}
	const lockKey = 727001
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			continue
		}
		var locked bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&locked); err == nil && locked {
			n, err := s.SyncDue(ctx, interval, 200)
			if err != nil {
				slog.ErrorContext(ctx, "courier sync failed", "err", err)
			} else if n > 0 {
				slog.InfoContext(ctx, "courier statuses synced", "count", n)
			}
			_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockKey)
		}
		conn.Release()
	}
}

func (s *Service) Balance(ctx context.Context) (float64, error) {
	if s.client == nil {
		return 0, ErrNotConfigured
	}
	return s.client.Balance(ctx)
}

// FraudCheck returns the customer's delivery history across Steadfast (delivery eligibility).
func (s *Service) FraudCheck(ctx context.Context, phone string) (*steadfast.FraudCheck, error) {
	if s.client == nil {
		return nil, ErrNotConfigured
	}
	return s.client.FraudCheck(ctx, phone)
}
