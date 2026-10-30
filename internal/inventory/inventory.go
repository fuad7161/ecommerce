// Package inventory performs transaction-safe stock changes and records every movement.
//
// Model: each variant has stock (physically on hand) and reserved (held by orders that
// are placed but not yet shipped). available = stock - reserved.
//
//	place order  -> reserve: reserved += q
//	cancel       -> release: reserved -= q
//	ship         -> ship:    stock -= q, reserved -= q
//	return       -> return:  stock += q
//
// All functions must run inside a transaction; each update is a single conditional
// UPDATE, so concurrent orders can never oversell.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/fuad71/ecommerce/internal/db"
)

var ErrInsufficientStock = errors.New("insufficient stock")

type Movement struct {
	ID             int64
	Kind           string
	StockChange    int
	ReservedChange int
	StockAfter     int
	ReservedAfter  int
	OrderNumber    string
	AdminName      string
	Note           string
	CreatedAt      time.Time
}

type Ref struct {
	OrderID int64
	AdminID int64
	Note    string
}

func nullable(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

func record(ctx context.Context, tx pgx.Tx, variantID int64, kind string, ds, dr, stock, reserved int, ref Ref) error {
	_, err := tx.Exec(ctx, `INSERT INTO inventory_movements
		(variant_id, kind, stock_change, reserved_change, stock_after, reserved_after, order_id, admin_id, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		variantID, kind, ds, dr, stock, reserved, nullable(ref.OrderID), nullable(ref.AdminID), ref.Note)
	return err
}

// Reserve holds qty units for an order. Fails with ErrInsufficientStock if not enough is available.
func Reserve(ctx context.Context, tx pgx.Tx, variantID int64, qty int, ref Ref) error {
	var stock, reserved int
	err := tx.QueryRow(ctx, `UPDATE product_variants SET reserved = reserved + $2, updated_at = now()
		WHERE id = $1 AND active AND stock - reserved >= $2
		RETURNING stock, reserved`, variantID, qty).Scan(&stock, &reserved)
	if db.IsNoRows(err) {
		return ErrInsufficientStock
	}
	if err != nil {
		return err
	}
	return record(ctx, tx, variantID, "reserve", 0, qty, stock, reserved, ref)
}

// Release returns reserved units to available stock (order cancelled before shipping).
func Release(ctx context.Context, tx pgx.Tx, variantID int64, qty int, ref Ref) error {
	var stock, reserved int
	err := tx.QueryRow(ctx, `UPDATE product_variants SET reserved = reserved - $2, updated_at = now()
		WHERE id = $1 AND reserved >= $2 RETURNING stock, reserved`, variantID, qty).Scan(&stock, &reserved)
	if err != nil {
		return fmt.Errorf("release variant %d: %w", variantID, err)
	}
	return record(ctx, tx, variantID, "release", 0, -qty, stock, reserved, ref)
}

// Ship converts a reservation into a stock deduction (goods left the warehouse).
func Ship(ctx context.Context, tx pgx.Tx, variantID int64, qty int, ref Ref) error {
	var stock, reserved int
	err := tx.QueryRow(ctx, `UPDATE product_variants SET stock = stock - $2, reserved = reserved - $2, updated_at = now()
		WHERE id = $1 AND reserved >= $2 AND stock >= $2 RETURNING stock, reserved`, variantID, qty).Scan(&stock, &reserved)
	if err != nil {
		return fmt.Errorf("ship variant %d: %w", variantID, err)
	}
	return record(ctx, tx, variantID, "ship", -qty, -qty, stock, reserved, ref)
}

// Return puts shipped units back into stock.
func Return(ctx context.Context, tx pgx.Tx, variantID int64, qty int, ref Ref) error {
	var stock, reserved int
	err := tx.QueryRow(ctx, `UPDATE product_variants SET stock = stock + $2, updated_at = now()
		WHERE id = $1 RETURNING stock, reserved`, variantID, qty).Scan(&stock, &reserved)
	if err != nil {
		return fmt.Errorf("return variant %d: %w", variantID, err)
	}
	return record(ctx, tx, variantID, "return", qty, 0, stock, reserved, ref)
}

// Adjust changes on-hand stock by delta (manual correction, restock, damage...).
// Stock can never drop below what is already reserved.
func Adjust(ctx context.Context, tx pgx.Tx, variantID int64, delta int, kind string, ref Ref) error {
	if kind != "initial" {
		kind = "adjustment"
	}
	var stock, reserved int
	err := tx.QueryRow(ctx, `UPDATE product_variants SET stock = stock + $2, updated_at = now()
		WHERE id = $1 AND stock + $2 >= reserved RETURNING stock, reserved`, variantID, delta).Scan(&stock, &reserved)
	if db.IsNoRows(err) {
		return ErrInsufficientStock
	}
	if err != nil {
		return err
	}
	return record(ctx, tx, variantID, kind, delta, 0, stock, reserved, ref)
}

func History(ctx context.Context, q db.Querier, variantID int64, limit int) ([]Movement, error) {
	rows, err := q.Query(ctx, `SELECT m.id, m.kind, m.stock_change, m.reserved_change, m.stock_after, m.reserved_after,
			coalesce(o.order_number, ''), coalesce(a.name, ''), m.note, m.created_at
		FROM inventory_movements m
		LEFT JOIN orders o ON o.id = m.order_id
		LEFT JOIN admins a ON a.id = m.admin_id
		WHERE m.variant_id = $1 ORDER BY m.created_at DESC, m.id DESC LIMIT $2`, variantID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Movement, error) {
		var m Movement
		err := r.Scan(&m.ID, &m.Kind, &m.StockChange, &m.ReservedChange, &m.StockAfter, &m.ReservedAfter,
			&m.OrderNumber, &m.AdminName, &m.Note, &m.CreatedAt)
		return m, err
	})
}
