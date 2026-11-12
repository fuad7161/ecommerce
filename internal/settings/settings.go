// Package settings manages admin-editable store settings and delivery zones.
package settings

import (
	"context"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuad71/ecommerce/internal/db"
)

// Keys editable from the admin settings page, in display order.
var Keys = []string{
	"store_name", "store_tagline", "store_phone", "store_email", "store_address",
	"facebook_url", "instagram_url", "hero_title", "hero_subtitle",
	"free_delivery_threshold", "cod_enabled", "online_payment_enabled", "meta_description", "policy_text",
}

type Store map[string]string

func (s Store) Get(k string) string { return s[k] }

// FreeDeliveryThreshold returns the threshold in poisha; 0 disables free delivery.
func (s Store) FreeDeliveryThreshold() int64 {
	v, _ := strconv.ParseInt(s["free_delivery_threshold"], 10, 64)
	return v
}

func (s Store) CODEnabled() bool { return s["cod_enabled"] != "false" }

func (s Store) OnlinePaymentEnabled() bool { return s["online_payment_enabled"] != "false" }

type Zone struct {
	ID        int64
	Name      string
	Charge    int64
	SortOrder int
	Active    bool
}

// Service caches settings in memory; they are read on every page render.
type Service struct {
	pool  *pgxpool.Pool
	mu    sync.RWMutex
	cache Store
}

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func (s *Service) All(ctx context.Context) (Store, error) {
	s.mu.RLock()
	c := s.cache
	s.mu.RUnlock()
	if c != nil {
		return c, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM store_settings`)
	if err != nil {
		return nil, err
	}
	m := Store{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache = m
	s.mu.Unlock()
	return m, nil
}

func (s *Service) Save(ctx context.Context, values map[string]string) error {
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		for k, v := range values {
			_, err := tx.Exec(ctx, `INSERT INTO store_settings (key, value) VALUES ($1, $2)
				ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, k, v)
			if err != nil {
				return err
			}
		}
		return nil
	})
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
	return err
}

func (s *Service) Zones(ctx context.Context, activeOnly bool) ([]Zone, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, charge, sort_order, active FROM delivery_zones
		WHERE active OR NOT $1 ORDER BY sort_order, id`, activeOnly)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Zone, error) {
		var z Zone
		err := r.Scan(&z.ID, &z.Name, &z.Charge, &z.SortOrder, &z.Active)
		return z, err
	})
}

func (s *Service) Zone(ctx context.Context, q db.Querier, id int64) (Zone, error) {
	var z Zone
	err := q.QueryRow(ctx, `SELECT id, name, charge, sort_order, active FROM delivery_zones WHERE id = $1`, id).
		Scan(&z.ID, &z.Name, &z.Charge, &z.SortOrder, &z.Active)
	return z, err
}

func (s *Service) SaveZone(ctx context.Context, z Zone) error {
	if z.ID == 0 {
		_, err := s.pool.Exec(ctx, `INSERT INTO delivery_zones (name, charge, sort_order, active) VALUES ($1, $2, $3, $4)`,
			z.Name, z.Charge, z.SortOrder, z.Active)
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE delivery_zones SET name = $2, charge = $3, sort_order = $4, active = $5 WHERE id = $1`,
		z.ID, z.Name, z.Charge, z.SortOrder, z.Active)
	return err
}
