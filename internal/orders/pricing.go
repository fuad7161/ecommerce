package orders

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/fuad71/ecommerce/internal/db"
)

// MaxQtyPerLine caps a single cart line to limit abuse.
const MaxQtyPerLine = 50

// CartLine is what the browser sends: only a variant SKU and a quantity.
// Prices, discounts and delivery fees are always computed server-side.
type CartLine struct {
	SKU string `json:"sku"`
	Qty int    `json:"qty"`
}

type QuoteLine struct {
	SKU          string `json:"sku"`
	VariantID    int64  `json:"-"`
	ProductID    int64  `json:"-"`
	ProductName  string `json:"product_name"`
	ProductSlug  string `json:"product_slug"`
	VariantName  string `json:"variant_name"`
	ImageKey     string `json:"image_key"`
	OfferTitle   string `json:"offer_title,omitempty"`
	RegularPrice int64  `json:"regular_price"`
	UnitPrice    int64  `json:"unit_price"`
	Qty          int    `json:"qty"`
	RequestedQty int    `json:"requested_qty"`
	Available    int    `json:"available"`
	LineTotal    int64  `json:"line_total"`
	Problem      string `json:"problem,omitempty"`
}

type Quote struct {
	Lines          []QuoteLine `json:"lines"`
	Subtotal       int64       `json:"subtotal"` // sum of regular prices
	Discount       int64       `json:"discount"`
	DeliveryCharge int64       `json:"delivery_charge"`
	Total          int64       `json:"total"`
	ZoneID         int64       `json:"zone_id"`
	ZoneName       string      `json:"zone_name"`
	FreeDelivery   bool        `json:"free_delivery"`
	ItemCount      int         `json:"item_count"`
	HasProblems    bool        `json:"has_problems"`
}

// Normalized returns the cart the browser should store after server validation.
func (q *Quote) Normalized() []CartLine {
	out := make([]CartLine, 0, len(q.Lines))
	for _, l := range q.Lines {
		if l.Qty > 0 {
			out = append(out, CartLine{SKU: l.SKU, Qty: l.Qty})
		}
	}
	return out
}

// mergeCart combines duplicate SKUs and drops invalid quantities.
func mergeCart(items []CartLine) []CartLine {
	idx := map[string]int{}
	var out []CartLine
	for _, it := range items {
		sku := strings.ToUpper(strings.TrimSpace(it.SKU))
		if sku == "" || it.Qty <= 0 {
			continue
		}
		if i, ok := idx[sku]; ok {
			out[i].Qty += it.Qty
			continue
		}
		idx[sku] = len(out)
		out = append(out, CartLine{SKU: sku, Qty: it.Qty})
	}
	for i := range out {
		out[i].Qty = min(out[i].Qty, MaxQtyPerLine)
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

// computeTotals fills in line totals and the quote summary. It is pure so it can be unit tested.
// Free delivery applies when (subtotal - discount) reaches freeThreshold (0 disables it).
func computeTotals(q *Quote, zoneCharge, freeThreshold int64) {
	q.Subtotal, q.Discount, q.ItemCount, q.HasProblems = 0, 0, 0, false
	for i := range q.Lines {
		l := &q.Lines[i]
		if l.Problem != "" {
			q.HasProblems = true
		}
		l.LineTotal = l.UnitPrice * int64(l.Qty)
		q.Subtotal += l.RegularPrice * int64(l.Qty)
		q.Discount += (l.RegularPrice - l.UnitPrice) * int64(l.Qty)
		q.ItemCount += l.Qty
	}
	net := q.Subtotal - q.Discount
	q.DeliveryCharge = zoneCharge
	q.FreeDelivery = false
	if q.ItemCount == 0 {
		q.DeliveryCharge = 0
	} else if freeThreshold > 0 && net >= freeThreshold {
		q.DeliveryCharge = 0
		q.FreeDelivery = true
	}
	q.Total = net + q.DeliveryCharge
}

// Quote prices a cart against current catalog data. Lines that cannot be bought are kept
// with a Problem message and Qty clamped (possibly to 0), so the UI can explain what changed.
// zoneID 0 means "not chosen yet": no delivery charge is added.
func (s *Service) Quote(ctx context.Context, q db.Querier, items []CartLine, zoneID int64) (*Quote, error) {
	items = mergeCart(items)
	quote := &Quote{Lines: []QuoteLine{}}

	var zoneCharge int64
	if zoneID > 0 {
		z, err := s.settings.Zone(ctx, q, zoneID)
		if err != nil || !z.Active {
			return nil, ErrInvalidZone
		}
		quote.ZoneID, quote.ZoneName, zoneCharge = z.ID, z.Name, z.Charge
	}
	if len(items) == 0 {
		computeTotals(quote, zoneCharge, 0)
		return quote, nil
	}

	skus := make([]string, len(items))
	for i, it := range items {
		skus[i] = it.SKU
	}
	rows, err := q.Query(ctx, `SELECT v.sku, v.id, v.name, v.active, greatest(v.stock - v.reserved, 0),
			p.id, p.name, p.slug, p.regular_price,
			LEAST(p.selling_price, coalesce(bo.offer_price, p.selling_price)),
			CASE WHEN bo.offer_price < p.selling_price THEN bo.offer_title ELSE '' END,
			(p.status = 'active' AND p.deleted_at IS NULL),
			coalesce((SELECT i.object_key FROM product_images i WHERE i.product_id = p.id ORDER BY i.sort_order, i.id LIMIT 1), '')
		FROM product_variants v JOIN products p ON p.id = v.product_id
		LEFT JOIN LATERAL best_offer(p.id, p.category_id, p.regular_price) bo ON true
		WHERE v.sku = ANY($1)`, skus)
	if err != nil {
		return nil, err
	}
	type row struct {
		line          QuoteLine
		variantActive bool
		productActive bool
	}
	found := map[string]row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.line.SKU, &r.line.VariantID, &r.line.VariantName, &r.variantActive, &r.line.Available,
			&r.line.ProductID, &r.line.ProductName, &r.line.ProductSlug, &r.line.RegularPrice, &r.line.UnitPrice,
			&r.line.OfferTitle, &r.productActive, &r.line.ImageKey); err != nil {
			rows.Close()
			return nil, err
		}
		found[r.line.SKU] = r
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, it := range items {
		r, ok := found[it.SKU]
		if !ok {
			quote.Lines = append(quote.Lines, QuoteLine{SKU: it.SKU, ProductName: "Unknown item", RequestedQty: it.Qty,
				Problem: "This item is no longer available"})
			continue
		}
		l := r.line
		l.RequestedQty = it.Qty
		l.Qty = it.Qty
		switch {
		case !r.variantActive || !r.productActive:
			l.Qty, l.Problem = 0, "This item is no longer available"
		case l.Available == 0:
			l.Qty, l.Problem = 0, "Out of stock"
		case it.Qty > l.Available:
			l.Qty, l.Problem = l.Available, fmt.Sprintf("Only %d left in stock", l.Available)
		}
		quote.Lines = append(quote.Lines, l)
	}

	st, err := s.settings.All(ctx)
	if err != nil {
		return nil, err
	}
	computeTotals(quote, zoneCharge, st.FreeDeliveryThreshold())
	return quote, nil
}

// lockVariants takes row locks on all variants in a cart, in a stable order, to avoid deadlocks
// between concurrent checkouts touching the same items.
func lockVariants(ctx context.Context, tx pgx.Tx, items []CartLine) error {
	skus := make([]string, len(items))
	for i, it := range items {
		skus[i] = it.SKU
	}
	_, err := tx.Exec(ctx, `SELECT id FROM product_variants WHERE sku = ANY($1) ORDER BY id FOR UPDATE`, skus)
	return err
}
