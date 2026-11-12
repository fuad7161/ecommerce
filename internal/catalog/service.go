// Package catalog manages categories, products, variants and product images.
package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuad71/ecommerce/internal/db"
	"github.com/fuad71/ecommerce/internal/inventory"
	"github.com/fuad71/ecommerce/internal/slug"
)

var (
	ErrNotFound   = errors.New("not found")
	ErrSKUTaken   = errors.New("SKU is already used by another product or variant")
	ErrSlugTaken  = errors.New("slug is already used")
	ErrValidation = errors.New("validation failed")
)

// ValidationError carries per-field messages for forms.
type ValidationError map[string]string

func (v ValidationError) Error() string {
	parts := make([]string, 0, len(v))
	for k, m := range v {
		parts = append(parts, k+": "+m)
	}
	slices.Sort(parts)
	return strings.Join(parts, "; ")
}
func (v ValidationError) Unwrap() error { return ErrValidation }

type Service struct{ pool *pgxpool.Pool }

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// ---------- Categories ----------

const categoryCols = `c.id, c.parent_id, coalesce(p.name, ''), c.name, c.slug, c.description, c.image_key,
	c.sort_order, c.active,
	(SELECT count(*) FROM products pr WHERE pr.category_id = c.id AND pr.deleted_at IS NULL)`

func scanCategory(r pgx.Row) (Category, error) {
	var c Category
	err := r.Scan(&c.ID, &c.ParentID, &c.ParentName, &c.Name, &c.Slug, &c.Description, &c.ImageKey,
		&c.SortOrder, &c.Active, &c.ProductCount)
	return c, err
}

// Categories returns a flat list ordered parents-first, each followed by its children.
func (s *Service) Categories(ctx context.Context, activeOnly bool) ([]Category, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+categoryCols+`
		FROM categories c LEFT JOIN categories p ON p.id = c.parent_id
		WHERE c.deleted_at IS NULL AND (c.active OR NOT $1)
		ORDER BY c.sort_order, c.name`, activeOnly)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Category, error) { return scanCategory(r) })
	if err != nil {
		return nil, err
	}
	var out []Category
	for _, c := range all {
		if c.ParentID == nil {
			out = append(out, c)
			for _, ch := range all {
				if ch.ParentID != nil && *ch.ParentID == c.ID {
					out = append(out, ch)
				}
			}
		}
	}
	// Orphans whose parent is inactive/deleted.
	for _, c := range all {
		if c.ParentID != nil && !slices.ContainsFunc(out, func(o Category) bool { return o.ID == c.ID }) {
			out = append(out, c)
		}
	}
	return out, nil
}

// CategoryTree returns active top-level categories with their active children attached.
func (s *Service) CategoryTree(ctx context.Context) ([]Category, error) {
	flat, err := s.Categories(ctx, true)
	if err != nil {
		return nil, err
	}
	var roots []Category
	for _, c := range flat {
		if c.ParentID == nil {
			roots = append(roots, c)
		}
	}
	for i := range roots {
		for _, c := range flat {
			if c.ParentID != nil && *c.ParentID == roots[i].ID {
				roots[i].Children = append(roots[i].Children, c)
			}
		}
	}
	return roots, nil
}

func (s *Service) CategoryBySlug(ctx context.Context, sl string) (*Category, error) {
	c, err := scanCategory(s.pool.QueryRow(ctx, `SELECT `+categoryCols+`
		FROM categories c LEFT JOIN categories p ON p.id = c.parent_id
		WHERE c.slug = $1 AND c.deleted_at IS NULL AND c.active`, sl))
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	return &c, err
}

func (s *Service) CategoryByID(ctx context.Context, id int64) (*Category, error) {
	c, err := scanCategory(s.pool.QueryRow(ctx, `SELECT `+categoryCols+`
		FROM categories c LEFT JOIN categories p ON p.id = c.parent_id
		WHERE c.id = $1 AND c.deleted_at IS NULL`, id))
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	return &c, err
}

func (s *Service) SaveCategory(ctx context.Context, c *Category) error {
	verr := ValidationError{}
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		verr["name"] = "Name is required"
	}
	c.Slug = slug.Make(c.Slug)
	if c.Slug == "" {
		c.Slug = slug.Make(c.Name)
	}
	if c.ParentID != nil && *c.ParentID == c.ID && c.ID != 0 {
		verr["parent_id"] = "A category cannot be its own parent"
	}
	if c.ParentID != nil {
		// Two-level hierarchy only: the parent must itself be top-level.
		var grand *int64
		err := s.pool.QueryRow(ctx, `SELECT parent_id FROM categories WHERE id = $1 AND deleted_at IS NULL`, *c.ParentID).Scan(&grand)
		if err != nil {
			verr["parent_id"] = "Parent category not found"
		} else if grand != nil {
			verr["parent_id"] = "Only two levels of categories are supported"
		}
	}
	if len(verr) > 0 {
		return verr
	}
	var err error
	if c.ID == 0 {
		err = s.pool.QueryRow(ctx, `INSERT INTO categories (parent_id, name, slug, description, image_key, sort_order, active)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
			c.ParentID, c.Name, c.Slug, c.Description, c.ImageKey, c.SortOrder, c.Active).Scan(&c.ID)
	} else {
		_, err = s.pool.Exec(ctx, `UPDATE categories SET parent_id = $2, name = $3, slug = $4, description = $5,
			image_key = $6, sort_order = $7, active = $8, updated_at = now() WHERE id = $1 AND deleted_at IS NULL`,
			c.ID, c.ParentID, c.Name, c.Slug, c.Description, c.ImageKey, c.SortOrder, c.Active)
	}
	if db.IsUniqueViolation(err, "categories_slug_uq") {
		return ValidationError{"slug": "This slug is already used by another category"}
	}
	return err
}

// DeleteCategory soft-deletes a category and detaches its children and products.
func (s *Service) DeleteCategory(ctx context.Context, id int64) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE categories SET parent_id = NULL WHERE parent_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE products SET category_id = NULL WHERE category_id = $1`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE categories SET deleted_at = now(), slug = slug || '-deleted-' || id
			WHERE id = $1 AND deleted_at IS NULL`, id)
		return err
	})
}

// ---------- Product listing & search ----------

type queryBuilder struct {
	where []string
	args  []any
}

func (b *queryBuilder) add(cond string, args ...any) {
	// Replace each ? with the next positional parameter.
	for _, a := range args {
		b.args = append(b.args, a)
		cond = strings.Replace(cond, "?", "$"+strconv.Itoa(len(b.args)), 1)
	}
	b.where = append(b.where, cond)
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// fromProducts joins the best running campaign for each product; priceExpr is the effective price.
const (
	fromProducts = `products p LEFT JOIN categories c ON c.id = p.category_id
		LEFT JOIN LATERAL best_offer(p.id, p.category_id, p.regular_price) bo ON true`
	priceExpr = `LEAST(p.selling_price, coalesce(bo.offer_price, p.selling_price))`
	// offerTitleExpr is set only when the campaign actually beats the product's own price.
	offerTitleExpr = `CASE WHEN bo.offer_price < p.selling_price THEN bo.offer_title ELSE '' END`
)

const availableExpr = `coalesce((SELECT sum(v.stock - v.reserved) FROM product_variants v
	WHERE v.product_id = p.id AND v.active), 0)`

func (s *Service) ListProducts(ctx context.Context, lp ListParams) (Page[ProductCard], error) {
	if lp.PerPage <= 0 || lp.PerPage > 100 {
		lp.PerPage = 24
	}
	if lp.Page < 1 {
		lp.Page = 1
	}
	b := &queryBuilder{}
	b.add("p.deleted_at IS NULL")
	if lp.Admin {
		if slices.Contains(ProductStatuses, lp.Status) {
			b.add("p.status = ?", lp.Status)
		}
		switch lp.Stock {
		case "out":
			b.add(availableExpr + " <= 0")
		case "low":
			b.add(`EXISTS (SELECT 1 FROM product_variants v WHERE v.product_id = p.id AND v.active
				AND v.stock - v.reserved <= v.low_stock_threshold)`)
		case "in":
			b.add(availableExpr + " > 0")
		}
	} else {
		b.add("p.status = 'active'")
		b.add("(p.category_id IS NULL OR EXISTS (SELECT 1 FROM categories c2 WHERE c2.id = p.category_id AND c2.active AND c2.deleted_at IS NULL))")
	}
	if q := strings.TrimSpace(lp.Q); q != "" {
		like := "%" + escapeLike(q) + "%"
		b.add("(p.search @@ plainto_tsquery('simple', ?) OR p.name ILIKE ? OR p.sku ILIKE ? OR p.brand ILIKE ?)", q, like, like, like)
	}
	if lp.CategorySlug != "" {
		b.add(`p.category_id IN (SELECT c3.id FROM categories c3 WHERE c3.deleted_at IS NULL AND
			(c3.slug = ? OR c3.parent_id = (SELECT c4.id FROM categories c4 WHERE c4.slug = ? AND c4.deleted_at IS NULL)))`,
			lp.CategorySlug, lp.CategorySlug)
	}
	if lp.CategoryID > 0 {
		b.add("p.category_id IN (SELECT c5.id FROM categories c5 WHERE c5.id = ? OR c5.parent_id = ?)", lp.CategoryID, lp.CategoryID)
	}
	if lp.MinPrice > 0 {
		b.add(priceExpr+" >= ?", lp.MinPrice)
	}
	if lp.MaxPrice > 0 {
		b.add(priceExpr+" <= ?", lp.MaxPrice)
	}
	if lp.InStock {
		b.add(availableExpr + " > 0")
	}
	if lp.OnSale {
		b.add(priceExpr + " < p.regular_price")
	}
	if lp.Featured {
		b.add("p.is_featured")
	}
	if lp.BestSeller {
		b.add("(p.is_best_seller OR p.sold_count > 0)")
	}
	if lp.OfferID > 0 {
		b.add(`(EXISTS (SELECT 1 FROM offer_products op WHERE op.offer_id = ? AND op.product_id = p.id)
			OR EXISTS (SELECT 1 FROM offer_categories oc WHERE oc.offer_id = ? AND (oc.category_id = p.category_id
				OR oc.category_id = (SELECT c6.parent_id FROM categories c6 WHERE c6.id = p.category_id))))`, lp.OfferID, lp.OfferID)
	}
	where := strings.Join(b.where, " AND ")

	order := "p.created_at DESC, p.id DESC"
	switch lp.Sort {
	case "price_asc":
		order = priceExpr + " ASC, p.id DESC"
	case "price_desc":
		order = priceExpr + " DESC, p.id DESC"
	case "popular":
		order = "p.is_featured DESC, p.sold_count DESC, p.id DESC"
	case "best_selling":
		order = "p.is_best_seller DESC, p.sold_count DESC, p.id DESC"
	case "trending":
		// Units sold in the last 30 days, excluding cancelled/returned orders.
		order = `(SELECT coalesce(sum(oi.quantity), 0) FROM order_items oi JOIN orders o ON o.id = oi.order_id
			WHERE oi.product_id = p.id AND o.created_at > now() - interval '30 days'
			AND o.status NOT IN ('cancelled', 'returned')) DESC, p.sold_count DESC, p.id DESC`
	case "name":
		order = "p.name ASC"
	case "stock_asc":
		order = availableExpr + " ASC, p.id DESC"
	}

	var page Page[ProductCard]
	page.Page, page.PerPage = lp.Page, lp.PerPage
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM "+fromProducts+" WHERE "+where, b.args...).Scan(&page.Total); err != nil {
		return page, err
	}

	args := append(b.args, lp.PerPage, (lp.Page-1)*lp.PerPage)
	n := len(b.args)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`SELECT p.id, p.name, p.slug, p.sku, p.status, coalesce(c.name, ''),
			p.regular_price, p.selling_price, `+priceExpr+`, `+offerTitleExpr+`, %s,
			coalesce((SELECT sum(v.stock) FROM product_variants v WHERE v.product_id = p.id AND v.active), 0),
			EXISTS (SELECT 1 FROM product_variants v WHERE v.product_id = p.id AND v.active
				AND v.stock - v.reserved <= v.low_stock_threshold),
			coalesce((SELECT i.object_key FROM product_images i WHERE i.product_id = p.id ORDER BY i.sort_order, i.id LIMIT 1), ''),
			p.is_featured, p.is_best_seller, p.sold_count, p.created_at
		FROM `+fromProducts+`
		WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`, availableExpr, where, order, n+1, n+2), args...)
	if err != nil {
		return page, err
	}
	page.Items, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ProductCard, error) {
		var c ProductCard
		err := r.Scan(&c.ID, &c.Name, &c.Slug, &c.SKU, &c.Status, &c.CategoryName, &c.RegularPrice, &c.SellingPrice,
			&c.Price, &c.OfferTitle, &c.Available, &c.Stock, &c.LowStock, &c.ImageKey, &c.IsFeatured, &c.IsBestSeller, &c.SoldCount, &c.CreatedAt)
		return c, err
	})
	return page, err
}

// ---------- Single product ----------

func (s *Service) loadProduct(ctx context.Context, where string, arg any) (*Product, error) {
	var p Product
	var specs []byte
	err := s.pool.QueryRow(ctx, `SELECT p.id, p.category_id, coalesce(c.name, ''), coalesce(c.slug, ''),
			p.name, p.slug, p.sku, p.brand, p.description, p.specs, p.regular_price, p.selling_price,
			`+priceExpr+`, `+offerTitleExpr+`, CASE WHEN bo.offer_price < p.selling_price THEN bo.offer_slug ELSE '' END,
			CASE WHEN bo.offer_price < p.selling_price THEN bo.offer_ends_at END,
			p.status, p.is_featured, p.is_best_seller, p.sold_count, p.created_at, p.updated_at
		FROM `+fromProducts+`
		WHERE p.deleted_at IS NULL AND `+where, arg).
		Scan(&p.ID, &p.CategoryID, &p.CategoryName, &p.CategorySlug, &p.Name, &p.Slug, &p.SKU, &p.Brand,
			&p.Description, &specs, &p.RegularPrice, &p.SellingPrice, &p.Price, &p.OfferTitle, &p.OfferSlug, &p.OfferEndsAt, &p.Status, &p.IsFeatured, &p.IsBestSeller,
			&p.SoldCount, &p.CreatedAt, &p.UpdatedAt)
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(specs, &p.Specs)

	if p.Images, err = s.Images(ctx, p.ID); err != nil {
		return nil, err
	}
	if p.Variants, err = s.Variants(ctx, p.ID); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Service) ProductBySlug(ctx context.Context, sl string, publicOnly bool) (*Product, error) {
	p, err := s.loadProduct(ctx, "p.slug = $1", sl)
	if err != nil {
		return nil, err
	}
	if publicOnly && p.Status != StatusActive {
		return nil, ErrNotFound
	}
	return p, nil
}

func (s *Service) ProductByID(ctx context.Context, id int64) (*Product, error) {
	return s.loadProduct(ctx, "p.id = $1", id)
}

func (s *Service) Images(ctx context.Context, productID int64) ([]Image, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, object_key, sort_order FROM product_images
		WHERE product_id = $1 ORDER BY sort_order, id`, productID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Image, error) {
		var i Image
		err := r.Scan(&i.ID, &i.Key, &i.SortOrder)
		return i, err
	})
}

func (s *Service) Variants(ctx context.Context, productID int64) ([]Variant, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, product_id, sku, name, stock, reserved, low_stock_threshold, sort_order, active
		FROM product_variants WHERE product_id = $1 ORDER BY sort_order, id`, productID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Variant, error) {
		var v Variant
		err := r.Scan(&v.ID, &v.ProductID, &v.SKU, &v.Name, &v.Stock, &v.Reserved, &v.LowStockThreshold, &v.SortOrder, &v.Active)
		return v, err
	})
}

func (s *Service) Variant(ctx context.Context, id int64) (*Variant, error) {
	var v Variant
	err := s.pool.QueryRow(ctx, `SELECT id, product_id, sku, name, stock, reserved, low_stock_threshold, sort_order, active
		FROM product_variants WHERE id = $1`, id).
		Scan(&v.ID, &v.ProductID, &v.SKU, &v.Name, &v.Stock, &v.Reserved, &v.LowStockThreshold, &v.SortOrder, &v.Active)
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	return &v, err
}

// Related returns active products from the same category (or newest products as a fallback).
func (s *Service) Related(ctx context.Context, p *Product, limit int) ([]ProductCard, error) {
	lp := ListParams{PerPage: limit + 1, Sort: "best_selling"}
	if p.CategorySlug != "" {
		lp.CategorySlug = p.CategorySlug
	}
	page, err := s.ListProducts(ctx, lp)
	if err != nil {
		return nil, err
	}
	out := slices.DeleteFunc(page.Items, func(c ProductCard) bool { return c.ID == p.ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---------- Admin writes ----------

// ParseSpecs reads "Name: Value" lines from a textarea.
func ParseSpecs(text string) []Spec {
	var out []Spec
	for line := range strings.SplitSeq(text, "\n") {
		k, v, ok := strings.Cut(line, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if ok && k != "" && v != "" {
			out = append(out, Spec{Name: k, Value: v})
		}
	}
	return out
}

func FormatSpecs(specs []Spec) string {
	var b strings.Builder
	for _, s := range specs {
		b.WriteString(s.Name + ": " + s.Value + "\n")
	}
	return b.String()
}

func (in *ProductInput) validate() ValidationError {
	verr := ValidationError{}
	in.Name = strings.TrimSpace(in.Name)
	in.SKU = strings.ToUpper(strings.TrimSpace(in.SKU))
	in.Brand = strings.TrimSpace(in.Brand)
	if in.Name == "" {
		verr["name"] = "Name is required"
	} else if len(in.Name) > 200 {
		verr["name"] = "Name is too long"
	}
	if in.SKU == "" {
		in.SKU = "SKU-" + strings.ToUpper(randHex(4))
	}
	if in.RegularPrice <= 0 {
		verr["regular_price"] = "Regular price must be greater than zero"
	}
	if in.SellingPrice <= 0 {
		verr["selling_price"] = "Selling price must be greater than zero"
	} else if in.SellingPrice > in.RegularPrice {
		verr["selling_price"] = "Selling price cannot exceed the regular price"
	}
	if !slices.Contains(ProductStatuses, in.Status) {
		verr["status"] = "Invalid status"
	}
	if in.InitialStock < 0 {
		verr["initial_stock"] = "Stock cannot be negative"
	}
	in.Slug = slug.Make(in.Slug)
	if in.Slug == "" {
		in.Slug = slug.Make(in.Name)
	}
	return verr
}

// SaveProduct creates or updates a product. On create, a default variant holding the initial stock is added.
func (s *Service) SaveProduct(ctx context.Context, adminID int64, in *ProductInput) error {
	if verr := in.validate(); len(verr) > 0 {
		return verr
	}
	specs, _ := json.Marshal(in.Specs)
	if in.Specs == nil {
		specs = []byte("[]")
	}

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM product_variants v JOIN products p ON p.id = v.product_id
			WHERE v.sku = $1 AND p.id <> $2)`, in.SKU, in.ID).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ValidationError{"sku": ErrSKUTaken.Error()}
		}

		if in.ID == 0 {
			if err := uniqueSlug(ctx, tx, &in.Slug); err != nil {
				return err
			}
			err := tx.QueryRow(ctx, `INSERT INTO products (category_id, name, slug, sku, brand, description, specs,
					regular_price, selling_price, status, is_featured, is_best_seller)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
				in.CategoryID, in.Name, in.Slug, in.SKU, in.Brand, in.Description, specs,
				in.RegularPrice, in.SellingPrice, in.Status, in.IsFeatured, in.IsBestSeller).Scan(&in.ID)
			if err != nil {
				return err
			}
			var variantID int64
			if err := tx.QueryRow(ctx, `INSERT INTO product_variants (product_id, sku, name) VALUES ($1, $2, '') RETURNING id`,
				in.ID, in.SKU).Scan(&variantID); err != nil {
				return err
			}
			if in.InitialStock > 0 {
				return inventory.Adjust(ctx, tx, variantID, in.InitialStock, "initial",
					inventory.Ref{AdminID: adminID, Note: "Initial stock"})
			}
			return nil
		}

		tag, err := tx.Exec(ctx, `UPDATE products SET category_id = $2, name = $3, slug = $4, sku = $5, brand = $6,
				description = $7, specs = $8, regular_price = $9, selling_price = $10, status = $11,
				is_featured = $12, is_best_seller = $13, updated_at = now()
			WHERE id = $1 AND deleted_at IS NULL`,
			in.ID, in.CategoryID, in.Name, in.Slug, in.SKU, in.Brand, in.Description, specs,
			in.RegularPrice, in.SellingPrice, in.Status, in.IsFeatured, in.IsBestSeller)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	switch {
	case db.IsUniqueViolation(err, "products_slug_uq"):
		return ValidationError{"slug": "This slug is already used by another product"}
	case db.IsUniqueViolation(err, "products_sku_uq"), db.IsUniqueViolation(err, "product_variants_sku_uq"):
		return ValidationError{"sku": ErrSKUTaken.Error()}
	}
	return err
}

func uniqueSlug(ctx context.Context, q db.Querier, sl *string) error {
	base := *sl
	for i := 2; ; i++ {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM products WHERE slug = $1 AND deleted_at IS NULL)`, *sl).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return nil
		}
		*sl = fmt.Sprintf("%s-%d", base, i)
	}
}

// DeleteProduct soft-deletes a product. Order history keeps its snapshots.
func (s *Service) DeleteProduct(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE products SET deleted_at = now(), status = 'archived',
		slug = slug || '-deleted-' || id, sku = sku || '-DELETED-' || id WHERE id = $1 AND deleted_at IS NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Service) SetStatus(ctx context.Context, id int64, status string) error {
	if !slices.Contains(ProductStatuses, status) {
		return ValidationError{"status": "Invalid status"}
	}
	_, err := s.pool.Exec(ctx, `UPDATE products SET status = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, id, status)
	return err
}

func (s *Service) AddVariant(ctx context.Context, adminID, productID int64, name, sku string, stock, threshold int) error {
	name, sku = strings.TrimSpace(name), strings.ToUpper(strings.TrimSpace(sku))
	verr := ValidationError{}
	if name == "" {
		verr["variant_name"] = "Variant name is required (e.g. Red / XL)"
	}
	if sku == "" {
		verr["variant_sku"] = "Variant SKU is required"
	}
	if stock < 0 {
		verr["variant_stock"] = "Stock cannot be negative"
	}
	if len(verr) > 0 {
		return verr
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO product_variants (product_id, sku, name, low_stock_threshold, sort_order)
			VALUES ($1, $2, $3, $4, (SELECT coalesce(max(sort_order), 0) + 1 FROM product_variants WHERE product_id = $1))
			RETURNING id`, productID, sku, name, threshold).Scan(&id)
		if err != nil {
			return err
		}
		if stock > 0 {
			return inventory.Adjust(ctx, tx, id, stock, "initial", inventory.Ref{AdminID: adminID, Note: "Initial stock"})
		}
		return nil
	})
	if db.IsUniqueViolation(err, "product_variants_sku_uq") {
		return ValidationError{"variant_sku": ErrSKUTaken.Error()}
	}
	return err
}

func (s *Service) UpdateVariant(ctx context.Context, id int64, name string, threshold int, active bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE product_variants SET name = $2, low_stock_threshold = $3, active = $4, updated_at = now()
		WHERE id = $1`, id, strings.TrimSpace(name), max(threshold, 0), active)
	return err
}

func (s *Service) AdjustStock(ctx context.Context, adminID, variantID int64, delta int, note string) error {
	if delta == 0 {
		return ValidationError{"delta": "Enter a non-zero quantity"}
	}
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		return inventory.Adjust(ctx, tx, variantID, delta, "adjustment", inventory.Ref{AdminID: adminID, Note: note})
	})
	if errors.Is(err, inventory.ErrInsufficientStock) {
		return ValidationError{"delta": "Stock cannot go below zero or below reserved quantity"}
	}
	return err
}

func (s *Service) AddImage(ctx context.Context, productID int64, key string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO product_images (product_id, object_key, sort_order)
		VALUES ($1, $2, (SELECT coalesce(max(sort_order), 0) + 1 FROM product_images WHERE product_id = $1))`, productID, key)
	return err
}

// DeleteImage removes the image row and returns its object key so the caller can delete the file.
func (s *Service) DeleteImage(ctx context.Context, productID, imageID int64) (string, error) {
	var key string
	err := s.pool.QueryRow(ctx, `DELETE FROM product_images WHERE id = $1 AND product_id = $2 RETURNING object_key`,
		imageID, productID).Scan(&key)
	if db.IsNoRows(err) {
		return "", ErrNotFound
	}
	return key, err
}

// MakePrimaryImage moves an image to the front of the gallery.
func (s *Service) MakePrimaryImage(ctx context.Context, productID, imageID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE product_images SET sort_order = CASE WHEN id = $2 THEN 0 ELSE sort_order + 1 END
		WHERE product_id = $1`, productID, imageID)
	return err
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Brief is a minimal product reference for pickers (e.g. choosing campaign products).
type Brief struct {
	ID     int64
	Name   string
	SKU    string
	Status string
}

func (s *Service) Briefs(ctx context.Context) ([]Brief, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, sku, status FROM products WHERE deleted_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Brief, error) {
		var b Brief
		err := r.Scan(&b.ID, &b.Name, &b.SKU, &b.Status)
		return b, err
	})
}
