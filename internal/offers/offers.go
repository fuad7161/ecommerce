// Package offers manages promotional campaigns (percentage or fixed discounts on products
// and categories within a date window). Pricing itself is applied in SQL by best_offer().
package offers

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuad71/ecommerce/internal/db"
	"github.com/fuad71/ecommerce/internal/slug"
)

var ErrNotFound = errors.New("offer not found")

const (
	TypePercent = "percent"
	TypeFixed   = "fixed"
)

type Offer struct {
	ID            int64
	Title         string
	Slug          string
	Description   string
	BannerKey     string
	DiscountType  string
	DiscountValue int64 // percent 1..90, or poisha for fixed
	StartsAt      time.Time
	EndsAt        time.Time
	Active        bool
	ShowOnHome    bool
	SortOrder     int
	ProductIDs    []int64
	CategoryIDs   []int64
	ProductCount  int
	CreatedAt     time.Time
}

// State is a human-readable lifecycle label for the admin list.
func (o *Offer) State(now time.Time) string {
	switch {
	case !o.Active:
		return "inactive"
	case now.Before(o.StartsAt):
		return "scheduled"
	case !now.Before(o.EndsAt):
		return "expired"
	}
	return "running"
}

func (o *Offer) Running() bool { return o.State(time.Now()) == "running" }

func (o *Offer) HasProduct(id int64) bool  { return contains(o.ProductIDs, id) }
func (o *Offer) HasCategory(id int64) bool { return contains(o.CategoryIDs, id) }

func contains(list []int64, id int64) bool {
	return slices.Contains(list, id)
}

// ValidationError maps form fields to messages.
type ValidationError map[string]string

func (v ValidationError) Error() string {
	parts := make([]string, 0, len(v))
	for k, m := range v {
		parts = append(parts, k+": "+m)
	}
	return strings.Join(parts, "; ")
}

type Service struct{ pool *pgxpool.Pool }

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Validate checks business rules. isNew additionally rejects campaigns that have already ended.
func Validate(o *Offer, isNew bool, now time.Time) ValidationError {
	v := ValidationError{}
	o.Title = strings.TrimSpace(o.Title)
	if o.Title == "" {
		v["title"] = "Title is required"
	}
	o.Slug = slug.Make(o.Slug)
	if o.Slug == "" {
		o.Slug = slug.Make(o.Title)
	}
	switch o.DiscountType {
	case TypePercent:
		if o.DiscountValue < 1 || o.DiscountValue > 90 {
			v["discount_value"] = "Percentage must be between 1 and 90"
		}
	case TypeFixed:
		if o.DiscountValue <= 0 {
			v["discount_value"] = "Discount amount must be greater than zero"
		}
	default:
		v["discount_type"] = "Choose percentage or fixed amount"
	}
	if o.StartsAt.IsZero() || o.EndsAt.IsZero() {
		v["dates"] = "Start and end date are required"
	} else if !o.EndsAt.After(o.StartsAt) {
		v["dates"] = "End date must be after the start date"
	} else if isNew && !o.EndsAt.After(now) {
		v["dates"] = "This campaign would already be expired"
	}
	if len(o.ProductIDs) == 0 && len(o.CategoryIDs) == 0 {
		v["targets"] = "Select at least one product or category"
	}
	return v
}

const cols = `o.id, o.title, o.slug, o.description, o.banner_key, o.discount_type, o.discount_value,
	o.starts_at, o.ends_at, o.active, o.show_on_home, o.sort_order, o.created_at`

func scan(r pgx.Row) (Offer, error) {
	var o Offer
	err := r.Scan(&o.ID, &o.Title, &o.Slug, &o.Description, &o.BannerKey, &o.DiscountType, &o.DiscountValue,
		&o.StartsAt, &o.EndsAt, &o.Active, &o.ShowOnHome, &o.SortOrder, &o.CreatedAt)
	return o, err
}

// List returns all campaigns for the admin.
func (s *Service) List(ctx context.Context) ([]Offer, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+cols+`,
			(SELECT count(*) FROM offer_products op WHERE op.offer_id = o.id)
		FROM offers o WHERE o.deleted_at IS NULL ORDER BY o.ends_at DESC, o.id DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Offer, error) {
		var o Offer
		err := r.Scan(&o.ID, &o.Title, &o.Slug, &o.Description, &o.BannerKey, &o.DiscountType, &o.DiscountValue,
			&o.StartsAt, &o.EndsAt, &o.Active, &o.ShowOnHome, &o.SortOrder, &o.CreatedAt, &o.ProductCount)
		return o, err
	})
}

// Running returns campaigns live right now; homeOnly limits to those promoted on the homepage.
func (s *Service) Running(ctx context.Context, homeOnly bool) ([]Offer, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+cols+` FROM offers o
		WHERE o.deleted_at IS NULL AND o.active AND now() >= o.starts_at AND now() < o.ends_at
			AND (o.show_on_home OR NOT $1)
		ORDER BY o.sort_order, o.ends_at`, homeOnly)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Offer, error) { return scan(r) })
}

func (s *Service) get(ctx context.Context, where string, arg any) (*Offer, error) {
	o, err := scan(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM offers o WHERE o.deleted_at IS NULL AND `+where, arg))
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT product_id FROM offer_products WHERE offer_id = $1`, o.ID)
	if err != nil {
		return nil, err
	}
	if o.ProductIDs, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `SELECT category_id FROM offer_categories WHERE offer_id = $1`, o.ID)
	if err != nil {
		return nil, err
	}
	o.CategoryIDs, err = pgx.CollectRows(rows, pgx.RowTo[int64])
	return &o, err
}

func (s *Service) Get(ctx context.Context, id int64) (*Offer, error) {
	return s.get(ctx, "o.id = $1", id)
}

// BySlug returns a campaign for the storefront only while it is running.
func (s *Service) BySlug(ctx context.Context, sl string) (*Offer, error) {
	o, err := s.get(ctx, "o.slug = $1", sl)
	if err != nil {
		return nil, err
	}
	if !o.Running() {
		return nil, ErrNotFound
	}
	return o, nil
}

func (s *Service) Save(ctx context.Context, o *Offer) error {
	if v := Validate(o, o.ID == 0, time.Now()); len(v) > 0 {
		return v
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if o.ID == 0 {
			if err := tx.QueryRow(ctx, `INSERT INTO offers (title, slug, description, banner_key, discount_type,
					discount_value, starts_at, ends_at, active, show_on_home, sort_order)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id`,
				o.Title, o.Slug, o.Description, o.BannerKey, o.DiscountType, o.DiscountValue, o.StartsAt, o.EndsAt,
				o.Active, o.ShowOnHome, o.SortOrder).Scan(&o.ID); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE offers SET title = $2, slug = $3, description = $4, banner_key = $5,
					discount_type = $6, discount_value = $7, starts_at = $8, ends_at = $9, active = $10,
					show_on_home = $11, sort_order = $12, updated_at = now()
				WHERE id = $1 AND deleted_at IS NULL`,
				o.ID, o.Title, o.Slug, o.Description, o.BannerKey, o.DiscountType, o.DiscountValue, o.StartsAt,
				o.EndsAt, o.Active, o.ShowOnHome, o.SortOrder)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrNotFound
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM offer_products WHERE offer_id = $1`, o.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM offer_categories WHERE offer_id = $1`, o.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO offer_products (offer_id, product_id)
			SELECT $1, id FROM products WHERE id = ANY($2) AND deleted_at IS NULL`, o.ID, o.ProductIDs); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO offer_categories (offer_id, category_id)
			SELECT $1, id FROM categories WHERE id = ANY($2) AND deleted_at IS NULL`, o.ID, o.CategoryIDs)
		return err
	})
	if db.IsUniqueViolation(err, "offers_slug_uq") {
		return ValidationError{"slug": "This slug is already used by another campaign"}
	}
	return err
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE offers SET deleted_at = now(), active = false,
		slug = slug || '-deleted-' || id WHERE id = $1 AND deleted_at IS NULL`, id)
	return err
}
