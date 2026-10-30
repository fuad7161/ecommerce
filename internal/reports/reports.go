// Package reports computes dashboard statistics from live database data.
package reports

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuad71/ecommerce/internal/orders"
)

// Revenue counts every order that is not cancelled or returned.
const revenueFilter = `status NOT IN ('cancelled', 'returned')`

type Overview struct {
	TotalRevenue   int64
	TodayRevenue   int64
	TotalOrders    int
	TodayOrders    int
	ByStatus       map[string]int
	TotalProducts  int
	LowStockCount  int
	OutOfStock     int
	BestSellers    []BestSeller
	RecentOrders   []orders.OrderRow
	DailySales     []DayPoint
	MaxDailyAmount int64
	LowStock       []LowStockItem
}

func (o Overview) Status(s string) int { return o.ByStatus[s] }

type BestSeller struct {
	Name     string
	Slug     string
	Quantity int
	Revenue  int64
}

type DayPoint struct {
	Day    time.Time
	Orders int
	Amount int64
}

type LowStockItem struct {
	ProductID   int64
	ProductName string
	VariantName string
	SKU         string
	Available   int
	Threshold   int
}

type Service struct{ pool *pgxpool.Pool }

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func (s *Service) Overview(ctx context.Context) (*Overview, error) {
	o := &Overview{ByStatus: map[string]int{}}
	loc := orders.Dhaka()
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	err := s.pool.QueryRow(ctx, `SELECT
			coalesce(sum(total) FILTER (WHERE `+revenueFilter+`), 0),
			coalesce(sum(total) FILTER (WHERE `+revenueFilter+` AND created_at >= $1), 0),
			count(*),
			count(*) FILTER (WHERE created_at >= $1)
		FROM orders`, today).Scan(&o.TotalRevenue, &o.TodayRevenue, &o.TotalOrders, &o.TodayOrders)
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `SELECT status, count(*) FROM orders GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		o.ByStatus[st] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	err = s.pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM products WHERE deleted_at IS NULL),
			(SELECT count(*) FROM product_variants v JOIN products p ON p.id = v.product_id
				WHERE p.deleted_at IS NULL AND v.active AND v.stock - v.reserved <= v.low_stock_threshold AND v.stock - v.reserved > 0),
			(SELECT count(*) FROM product_variants v JOIN products p ON p.id = v.product_id
				WHERE p.deleted_at IS NULL AND v.active AND v.stock - v.reserved <= 0)`).
		Scan(&o.TotalProducts, &o.LowStockCount, &o.OutOfStock)
	if err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `SELECT i.product_name, coalesce(p.slug, ''), sum(i.quantity), sum(i.line_total)
		FROM order_items i JOIN orders o ON o.id = i.order_id
		LEFT JOIN products p ON p.id = i.product_id
		WHERE o.`+revenueFilter+`
		GROUP BY i.product_name, p.slug ORDER BY sum(i.quantity) DESC LIMIT 5`)
	if err != nil {
		return nil, err
	}
	o.BestSellers, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (BestSeller, error) {
		var b BestSeller
		err := r.Scan(&b.Name, &b.Slug, &b.Quantity, &b.Revenue)
		return b, err
	})
	if err != nil {
		return nil, err
	}

	recent, err := (&orderLister{s.pool}).recent(ctx)
	if err != nil {
		return nil, err
	}
	o.RecentOrders = recent

	// Last 14 days of sales, one row per day including empty days.
	start := today.AddDate(0, 0, -13)
	rows, err = s.pool.Query(ctx, `SELECT d, count(o.id), coalesce(sum(o.total), 0)
		FROM generate_series($1::timestamptz, $2::timestamptz, interval '1 day') d
		LEFT JOIN orders o ON o.created_at >= d AND o.created_at < d + interval '1 day' AND o.`+revenueFilter+`
		GROUP BY d ORDER BY d`, start, today)
	if err != nil {
		return nil, err
	}
	o.DailySales, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (DayPoint, error) {
		var p DayPoint
		err := r.Scan(&p.Day, &p.Orders, &p.Amount)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	for _, p := range o.DailySales {
		o.MaxDailyAmount = max(o.MaxDailyAmount, p.Amount)
	}

	rows, err = s.pool.Query(ctx, `SELECT p.id, p.name, v.name, v.sku, v.stock - v.reserved, v.low_stock_threshold
		FROM product_variants v JOIN products p ON p.id = v.product_id
		WHERE p.deleted_at IS NULL AND v.active AND v.stock - v.reserved <= v.low_stock_threshold
		ORDER BY v.stock - v.reserved, p.name LIMIT 8`)
	if err != nil {
		return nil, err
	}
	o.LowStock, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (LowStockItem, error) {
		var l LowStockItem
		err := r.Scan(&l.ProductID, &l.ProductName, &l.VariantName, &l.SKU, &l.Available, &l.Threshold)
		return l, err
	})
	return o, err
}

type orderLister struct{ pool *pgxpool.Pool }

func (l *orderLister) recent(ctx context.Context) ([]orders.OrderRow, error) {
	rows, err := l.pool.Query(ctx, `SELECT o.id, o.order_number, o.customer_name, o.phone, o.district,
			o.status, o.payment_method, (SELECT coalesce(sum(quantity), 0) FROM order_items WHERE order_id = o.id),
			o.total, o.created_at
		FROM orders o ORDER BY o.created_at DESC LIMIT 8`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (orders.OrderRow, error) {
		var o orders.OrderRow
		err := r.Scan(&o.ID, &o.Number, &o.CustomerName, &o.Phone, &o.District, &o.Status, &o.Payment,
			&o.ItemCount, &o.Total, &o.CreatedAt)
		return o, err
	})
}
