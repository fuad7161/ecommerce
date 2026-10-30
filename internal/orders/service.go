// Package orders implements cart pricing, guest checkout, order tracking and order management.
package orders

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuad71/ecommerce/internal/db"
	"github.com/fuad71/ecommerce/internal/inventory"
	"github.com/fuad71/ecommerce/internal/money"
	"github.com/fuad71/ecommerce/internal/paging"
	"github.com/fuad71/ecommerce/internal/phone"
	"github.com/fuad71/ecommerce/internal/settings"
)

var dhaka = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Dhaka")
	if err != nil {
		return time.FixedZone("BST", 6*3600)
	}
	return loc
}()

func Dhaka() *time.Location { return dhaka }

type Service struct {
	pool     *pgxpool.Pool
	settings *settings.Service
}

func NewService(pool *pgxpool.Pool, st *settings.Service) *Service {
	return &Service{pool: pool, settings: st}
}

// ---------- Checkout ----------

func (in *CheckoutInput) validate() FieldErrors {
	fe := FieldErrors{}
	in.Name = strings.Join(strings.Fields(in.Name), " ")
	if n := utf8.RuneCountInString(in.Name); n < 2 || n > 80 {
		fe["name"] = "Please enter your full name (2–80 characters)"
	}
	if p, err := phone.Normalize(in.Phone); err != nil {
		fe["phone"] = err.Error()
	} else {
		in.Phone = p
	}
	in.District = strings.TrimSpace(in.District)
	in.Area = strings.TrimSpace(in.Area)
	in.Address = strings.TrimSpace(in.Address)
	in.Notes = strings.TrimSpace(in.Notes)
	if in.District == "" {
		fe["district"] = "District / city is required"
	}
	if in.Area == "" {
		fe["area"] = "Area / thana is required"
	}
	if n := utf8.RuneCountInString(in.Address); n < 10 || n > 300 {
		fe["address"] = "Please enter your full address (house, road, landmark)"
	}
	if utf8.RuneCountInString(in.Notes) > 300 {
		fe["notes"] = "Delivery instructions are too long"
	}
	if in.ZoneID <= 0 {
		fe["zone"] = "Please choose a delivery area"
	}
	if in.PaymentMethod != "cod" {
		fe["payment_method"] = "Please choose Cash on Delivery"
	}
	if !in.Confirmed {
		fe["confirm"] = "Please confirm your order details"
	}
	if l := len(in.IdempotencyKey); l < 16 || l > 100 {
		fe["form"] = "Your checkout session expired, please reload the page"
	}
	return fe
}

// PlaceOrder validates the checkout, re-prices the cart, reserves stock and creates the
// order atomically. Re-submitting the same idempotency key returns the original order.
func (s *Service) PlaceOrder(ctx context.Context, in CheckoutInput) (*Order, error) {
	if fe := in.validate(); len(fe) > 0 {
		return nil, fe
	}
	in.Items = mergeCart(in.Items)
	if len(in.Items) == 0 {
		return nil, ErrEmptyCart
	}
	if st, err := s.settings.All(ctx); err != nil {
		return nil, err
	} else if !st.CODEnabled() {
		return nil, ErrCODDisabled
	}

	if o, err := s.byIdempotencyKey(ctx, in.IdempotencyKey); err == nil {
		return o, nil
	}

	var orderID int64
	for attempt := 0; ; attempt++ {
		err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
			var err error
			orderID, err = s.createOrder(ctx, tx, in)
			return err
		})
		if err == nil {
			break
		}
		if db.IsUniqueViolation(err, "orders_idempotency_key_uq") {
			// A concurrent duplicate submission won the race.
			return s.byIdempotencyKey(ctx, in.IdempotencyKey)
		}
		if db.IsUniqueViolation(err, "orders_order_number_uq") && attempt < 5 {
			continue
		}
		return nil, err
	}
	return s.OrderByID(ctx, orderID)
}

func (s *Service) createOrder(ctx context.Context, tx pgx.Tx, in CheckoutInput) (int64, error) {
	if err := lockVariants(ctx, tx, in.Items); err != nil {
		return 0, err
	}
	quote, err := s.Quote(ctx, tx, in.Items, in.ZoneID)
	if err != nil {
		return 0, err
	}
	if quote.HasProblems {
		return 0, ErrCartChanged
	}

	var customerID int64
	err = tx.QueryRow(ctx, `INSERT INTO customers (phone, name, district, area, address) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (phone) DO UPDATE SET name = EXCLUDED.name, district = EXCLUDED.district, area = EXCLUDED.area,
			address = EXCLUDED.address, updated_at = now()
		RETURNING id`, in.Phone, in.Name, in.District, in.Area, in.Address).Scan(&customerID)
	if err != nil {
		return 0, err
	}

	var orderID int64
	err = tx.QueryRow(ctx, `INSERT INTO orders (order_number, idempotency_key, customer_id, customer_name, phone,
			delivery_zone_id, zone_name, district, area, address, delivery_notes, payment_method,
			subtotal, discount_total, delivery_charge, total)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'cod', $12, $13, $14, $15) RETURNING id`,
		newOrderNumber(), in.IdempotencyKey, customerID, in.Name, in.Phone, quote.ZoneID, quote.ZoneName,
		in.District, in.Area, in.Address, in.Notes,
		quote.Subtotal, quote.Discount, quote.DeliveryCharge, quote.Total).Scan(&orderID)
	if err != nil {
		return 0, err
	}

	ref := inventory.Ref{OrderID: orderID, Note: "Order placed"}
	for _, l := range quote.Lines {
		_, err := tx.Exec(ctx, `INSERT INTO order_items (order_id, product_id, variant_id, product_name, variant_name, sku,
				image_key, regular_price, unit_price, quantity, line_total, offer_title)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			orderID, l.ProductID, l.VariantID, l.ProductName, l.VariantName, l.SKU, l.ImageKey,
			l.RegularPrice, l.UnitPrice, l.Qty, l.LineTotal, l.OfferTitle)
		if err != nil {
			return 0, err
		}
		if err := inventory.Reserve(ctx, tx, l.VariantID, l.Qty, ref); err != nil {
			if errors.Is(err, inventory.ErrInsufficientStock) {
				return 0, ErrCartChanged
			}
			return 0, err
		}
	}

	_, err = tx.Exec(ctx, `INSERT INTO order_status_history (order_id, from_status, to_status, note)
		VALUES ($1, NULL, 'pending', 'Order placed by customer')`, orderID)
	return orderID, err
}

// newOrderNumber returns e.g. "261030-48291" (Dhaka date + 5 random digits).
// Uniqueness is enforced by the database; collisions are retried.
func newOrderNumber() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(100000))
	return fmt.Sprintf("%s-%05d", time.Now().In(dhaka).Format("060102"), n.Int64())
}

// ---------- Reads ----------

const orderCols = `o.id, o.order_number, o.customer_id, o.customer_name, o.phone, o.zone_name, o.district, o.area,
	o.address, o.delivery_notes, o.payment_method, o.payment_status, o.status, o.subtotal, o.discount_total,
	o.delivery_charge, o.total, o.created_at, o.updated_at`

func (s *Service) loadOrder(ctx context.Context, where string, args ...any) (*Order, error) {
	var o Order
	err := s.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM orders o WHERE `+where, args...).Scan(
		&o.ID, &o.Number, &o.CustomerID, &o.CustomerName, &o.Phone, &o.ZoneName, &o.District, &o.Area,
		&o.Address, &o.DeliveryNotes, &o.PaymentMethod, &o.PaymentStatus, &o.Status, &o.Subtotal, &o.Discount,
		&o.DeliveryCharge, &o.Total, &o.CreatedAt, &o.UpdatedAt)
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `SELECT i.product_id, coalesce(p.slug, ''), i.product_name, i.variant_name, i.sku,
			i.image_key, i.regular_price, i.unit_price, i.quantity, i.line_total, i.offer_title
		FROM order_items i LEFT JOIN products p ON p.id = i.product_id AND p.deleted_at IS NULL
		WHERE i.order_id = $1 ORDER BY i.id`, o.ID)
	if err != nil {
		return nil, err
	}
	o.Items, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		err := r.Scan(&it.ProductID, &it.ProductSlug, &it.ProductName, &it.VariantName, &it.SKU, &it.ImageKey,
			&it.RegularPrice, &it.UnitPrice, &it.Quantity, &it.LineTotal, &it.OfferTitle)
		return it, err
	})
	if err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `SELECT coalesce(h.from_status, ''), h.to_status, h.note, coalesce(a.name, ''), h.created_at
		FROM order_status_history h LEFT JOIN admins a ON a.id = h.admin_id
		WHERE h.order_id = $1 ORDER BY h.created_at, h.id`, o.ID)
	if err != nil {
		return nil, err
	}
	o.History, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (StatusChange, error) {
		var h StatusChange
		err := r.Scan(&h.From, &h.To, &h.Note, &h.AdminName, &h.CreatedAt)
		return h, err
	})
	return &o, err
}

func (s *Service) OrderByID(ctx context.Context, id int64) (*Order, error) {
	return s.loadOrder(ctx, "o.id = $1", id)
}

func (s *Service) OrderByNumber(ctx context.Context, number string) (*Order, error) {
	return s.loadOrder(ctx, "o.order_number = $1", strings.TrimSpace(number))
}

func (s *Service) byIdempotencyKey(ctx context.Context, key string) (*Order, error) {
	return s.loadOrder(ctx, "o.idempotency_key = $1", key)
}

// Track returns an order only when both the order number and the checkout phone match.
func (s *Service) Track(ctx context.Context, number, phoneNumber string) (*Order, error) {
	p, err := phone.Normalize(phoneNumber)
	if err != nil {
		return nil, ErrNotFound
	}
	number = strings.TrimPrefix(strings.TrimSpace(number), "#")
	return s.loadOrder(ctx, "o.order_number = $1 AND o.phone = $2", number, p)
}

func (s *Service) Notes(ctx context.Context, orderID int64) ([]Note, error) {
	rows, err := s.pool.Query(ctx, `SELECT coalesce(a.name, ''), n.body, n.created_at FROM order_notes n
		LEFT JOIN admins a ON a.id = n.admin_id WHERE n.order_id = $1 ORDER BY n.created_at DESC`, orderID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Note, error) {
		var n Note
		err := r.Scan(&n.AdminName, &n.Body, &n.CreatedAt)
		return n, err
	})
}

func (s *Service) listWhere(lp ListParams) (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	conds = append(conds, "TRUE")
	if q := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lp.Q), "#")); q != "" {
		if p, err := phone.Normalize(q); err == nil {
			add("o.phone = ?", p)
		} else {
			add("(o.order_number ILIKE ? OR o.customer_name ILIKE ? OR o.phone LIKE ?)", "%"+q+"%")
		}
	}
	if lp.Status != "" {
		add("o.status = ?", lp.Status)
	}
	if lp.Payment != "" {
		add("o.payment_method = ?", lp.Payment)
	}
	if !lp.From.IsZero() {
		add("o.created_at >= ?", lp.From)
	}
	if !lp.To.IsZero() {
		add("o.created_at < ?", lp.To.AddDate(0, 0, 1))
	}
	return strings.Join(conds, " AND "), args
}

func (s *Service) List(ctx context.Context, lp ListParams) (paging.Page[OrderRow], error) {
	var page paging.Page[OrderRow]
	var offset int
	page.Page, page.PerPage, offset = paging.Normalize(lp.Page, lp.PerPage, 25)
	where, args := s.listWhere(lp)
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM orders o WHERE "+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	n := len(args)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`SELECT o.id, o.order_number, o.customer_name, o.phone, o.district,
			o.status, o.payment_method, (SELECT coalesce(sum(quantity), 0) FROM order_items WHERE order_id = o.id),
			o.total, o.created_at
		FROM orders o WHERE %s ORDER BY o.created_at DESC, o.id DESC LIMIT $%d OFFSET $%d`, where, n+1, n+2),
		append(args, page.PerPage, offset)...)
	if err != nil {
		return page, err
	}
	page.Items, err = pgx.CollectRows(rows, scanOrderRow)
	return page, err
}

func scanOrderRow(r pgx.CollectableRow) (OrderRow, error) {
	var o OrderRow
	err := r.Scan(&o.ID, &o.Number, &o.CustomerName, &o.Phone, &o.District, &o.Status, &o.Payment, &o.ItemCount,
		&o.Total, &o.CreatedAt)
	return o, err
}

// ExportCSV streams all orders matching the filters.
func (s *Service) ExportCSV(ctx context.Context, w io.Writer, lp ListParams) error {
	where, args := s.listWhere(lp)
	rows, err := s.pool.Query(ctx, `SELECT o.order_number, o.created_at, o.status, o.customer_name, o.phone, o.zone_name,
			o.district, o.area, o.address, o.payment_method, o.subtotal, o.discount_total, o.delivery_charge, o.total,
			(SELECT string_agg(i.product_name || CASE WHEN i.variant_name <> '' THEN ' (' || i.variant_name || ')' ELSE '' END
				|| ' x' || i.quantity, '; ' ORDER BY i.id) FROM order_items i WHERE i.order_id = o.id)
		FROM orders o WHERE `+where+` ORDER BY o.created_at DESC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"Order", "Date", "Status", "Customer", "Phone", "Zone", "District", "Area", "Address",
		"Payment", "Subtotal", "Discount", "Delivery", "Total", "Items"})
	for rows.Next() {
		var (
			num, status, name, ph, zone, district, area, addr, pay, items string
			created                                                       time.Time
			sub, disc, del, total                                         int64
		)
		if err := rows.Scan(&num, &created, &status, &name, &ph, &zone, &district, &area, &addr, &pay,
			&sub, &disc, &del, &total, &items); err != nil {
			return err
		}
		_ = cw.Write(csvSafe([]string{num, created.In(dhaka).Format("2006-01-02 15:04"), StatusLabel(status), name, ph,
			zone, district, area, addr, strings.ToUpper(pay), money.Plain(sub), money.Plain(disc), money.Plain(del),
			money.Plain(total), items}))
	}
	cw.Flush()
	if err := rows.Err(); err != nil {
		return err
	}
	return cw.Error()
}

// csvSafe neutralises spreadsheet formula injection in customer-provided text.
func csvSafe(cells []string) []string {
	for i, c := range cells {
		if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
			cells[i] = "'" + c
		}
	}
	return cells
}

// ---------- Status management ----------

// UpdateStatus moves an order through the state machine and applies inventory effects:
// cancelled releases reservations, shipped deducts stock, returned restocks.
func (s *Service) UpdateStatus(ctx context.Context, adminID, orderID int64, to, note string) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var from string
		if err := tx.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1 FOR UPDATE`, orderID).Scan(&from); err != nil {
			if db.IsNoRows(err) {
				return ErrNotFound
			}
			return err
		}
		if !CanTransition(from, to) {
			return ErrInvalidTransition
		}

		type line struct {
			variantID int64
			qty       int
		}
		rows, err := tx.Query(ctx, `SELECT variant_id, quantity FROM order_items WHERE order_id = $1 AND variant_id IS NOT NULL
			ORDER BY variant_id`, orderID)
		if err != nil {
			return err
		}
		lines, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (line, error) {
			var l line
			err := r.Scan(&l.variantID, &l.qty)
			return l, err
		})
		if err != nil {
			return err
		}

		ref := inventory.Ref{OrderID: orderID, AdminID: adminID, Note: "Order " + StatusLabel(to)}
		for _, l := range lines {
			switch to {
			case StatusCancelled:
				err = inventory.Release(ctx, tx, l.variantID, l.qty, ref)
			case StatusShipped:
				err = inventory.Ship(ctx, tx, l.variantID, l.qty, ref)
			case StatusReturned:
				err = inventory.Return(ctx, tx, l.variantID, l.qty, ref)
			}
			if err != nil {
				return err
			}
		}
		switch to {
		case StatusShipped:
			_, err = tx.Exec(ctx, `UPDATE products p SET sold_count = sold_count + i.quantity
				FROM order_items i WHERE i.order_id = $1 AND i.product_id = p.id`, orderID)
		case StatusReturned:
			_, err = tx.Exec(ctx, `UPDATE products p SET sold_count = greatest(sold_count - i.quantity, 0)
				FROM order_items i WHERE i.order_id = $1 AND i.product_id = p.id`, orderID)
		}
		if err != nil {
			return err
		}

		payment := ""
		switch to {
		case StatusDelivered:
			payment = "paid" // COD collected on delivery
		case StatusReturned:
			payment = "refunded"
		}
		if _, err := tx.Exec(ctx, `UPDATE orders SET status = $2, updated_at = now(),
				payment_status = CASE WHEN $3 = '' THEN payment_status ELSE $3 END WHERE id = $1`,
			orderID, to, payment); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO order_status_history (order_id, from_status, to_status, note, admin_id)
			VALUES ($1, $2, $3, $4, NULLIF($5, 0))`, orderID, from, to, strings.TrimSpace(note), adminID)
		return err
	})
}

func (s *Service) AddNote(ctx context.Context, adminID, orderID int64, body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return FieldErrors{"note": "Note cannot be empty"}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO order_notes (order_id, admin_id, body) VALUES ($1, $2, $3)`, orderID, adminID, body)
	return err
}

// ---------- Customers ----------

func (s *Service) Customers(ctx context.Context, q string, page int) (paging.Page[Customer], error) {
	var p paging.Page[Customer]
	var offset int
	p.Page, p.PerPage, offset = paging.Normalize(page, 25, 25)
	q = strings.TrimSpace(q)
	cond, arg := "TRUE OR $1 = ''", any("")
	if q != "" {
		if ph, err := phone.Normalize(q); err == nil {
			cond, arg = "c.phone = $1", ph
		} else {
			cond, arg = "(c.phone LIKE $1 OR c.name ILIKE $1)", "%"+q+"%"
		}
	}
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM customers c WHERE "+cond, arg).Scan(&p.Total); err != nil {
		return p, err
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.phone, c.name, c.district, c.area, c.address, c.notes,
			count(o.id), coalesce(sum(o.total) FILTER (WHERE o.status NOT IN ('cancelled', 'returned')), 0),
			max(o.created_at), c.created_at
		FROM customers c LEFT JOIN orders o ON o.customer_id = c.id
		WHERE `+cond+` GROUP BY c.id ORDER BY max(o.created_at) DESC NULLS LAST LIMIT $2 OFFSET $3`, arg, p.PerPage, offset)
	if err != nil {
		return p, err
	}
	p.Items, err = pgx.CollectRows(rows, scanCustomer)
	return p, err
}

func scanCustomer(r pgx.CollectableRow) (Customer, error) {
	var c Customer
	err := r.Scan(&c.ID, &c.Phone, &c.Name, &c.District, &c.Area, &c.Address, &c.Notes,
		&c.OrderCount, &c.TotalSpent, &c.LastOrderAt, &c.CreatedAt)
	return c, err
}

func (s *Service) Customer(ctx context.Context, id int64) (*Customer, []OrderRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.phone, c.name, c.district, c.area, c.address, c.notes,
			count(o.id), coalesce(sum(o.total) FILTER (WHERE o.status NOT IN ('cancelled', 'returned')), 0),
			max(o.created_at), c.created_at
		FROM customers c LEFT JOIN orders o ON o.customer_id = c.id WHERE c.id = $1 GROUP BY c.id`, id)
	if err != nil {
		return nil, nil, err
	}
	c, err := pgx.CollectExactlyOneRow(rows, scanCustomer)
	if db.IsNoRows(err) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT o.id, o.order_number, o.customer_name, o.phone, o.district,
			o.status, o.payment_method, (SELECT coalesce(sum(quantity), 0) FROM order_items WHERE order_id = o.id),
			o.total, o.created_at
		FROM orders o WHERE o.customer_id = $1 ORDER BY o.created_at DESC`, id)
	if err != nil {
		return nil, nil, err
	}
	list, err := pgx.CollectRows(rows, scanOrderRow)
	return &c, list, err
}

func (s *Service) SaveCustomerNotes(ctx context.Context, id int64, notes string) error {
	_, err := s.pool.Exec(ctx, `UPDATE customers SET notes = $2, updated_at = now() WHERE id = $1`, id, strings.TrimSpace(notes))
	return err
}
