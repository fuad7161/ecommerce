package catalog

import (
	"time"

	"github.com/fuad71/ecommerce/internal/paging"
)

type Category struct {
	ID           int64
	ParentID     *int64
	ParentName   string
	Name         string
	Slug         string
	Description  string
	ImageKey     string
	SortOrder    int
	Active       bool
	ProductCount int
	Children     []Category
}

type Spec struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Image struct {
	ID        int64
	Key       string
	SortOrder int
}

type Variant struct {
	ID                int64
	ProductID         int64
	SKU               string
	Name              string
	Stock             int
	Reserved          int
	LowStockThreshold int
	SortOrder         int
	Active            bool
}

func (v Variant) Available() int { return max(v.Stock-v.Reserved, 0) }

// ProductCard is the summary row used by listings (storefront grid and admin table).
type ProductCard struct {
	ID           int64
	Name         string
	Slug         string
	SKU          string
	Status       string
	CategoryName string
	RegularPrice int64
	SellingPrice int64 // the product's own price (admin-managed)
	Price        int64 // what the customer pays: min(selling price, best running offer)
	OfferTitle   string
	Available    int
	Stock        int
	LowStock     bool
	ImageKey     string
	IsFeatured   bool
	IsBestSeller bool
	SoldCount    int
	CreatedAt    time.Time
}

func (p ProductCard) DiscountPercent() int { return discountPercent(p.RegularPrice, p.Price) }
func (p ProductCard) OnSale() bool         { return p.Price < p.RegularPrice }

type Product struct {
	ID           int64
	CategoryID   *int64
	CategoryName string
	CategorySlug string
	Name         string
	Slug         string
	SKU          string
	Brand        string
	Description  string
	Specs        []Spec
	RegularPrice int64
	SellingPrice int64
	Price        int64 // effective price, see ProductCard.Price
	OfferTitle   string
	OfferSlug    string
	OfferEndsAt  *time.Time
	Status       string
	IsFeatured   bool
	IsBestSeller bool
	SoldCount    int
	Images       []Image
	Variants     []Variant
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (p *Product) DiscountPercent() int { return discountPercent(p.RegularPrice, p.Price) }
func (p *Product) OnSale() bool         { return p.Price < p.RegularPrice }

// OfferApplied reports whether a campaign beats the product's own selling price.
func (p *Product) OfferApplied() bool { return p.OfferTitle != "" }

// ActiveVariants returns only variants a customer may buy.
func (p *Product) ActiveVariants() []Variant {
	var out []Variant
	for _, v := range p.Variants {
		if v.Active {
			out = append(out, v)
		}
	}
	return out
}

// HasOptions reports whether the customer must choose between named variants.
func (p *Product) HasOptions() bool {
	av := p.ActiveVariants()
	return len(av) > 1 || (len(av) == 1 && av[0].Name != "")
}

func (p *Product) Available() int {
	n := 0
	for _, v := range p.ActiveVariants() {
		n += v.Available()
	}
	return n
}

func discountPercent(regular, selling int64) int {
	if regular <= 0 || selling >= regular {
		return 0
	}
	return int((regular - selling) * 100 / regular)
}

const (
	StatusDraft    = "draft"
	StatusActive   = "active"
	StatusInactive = "inactive"
	StatusArchived = "archived"
)

var ProductStatuses = []string{StatusDraft, StatusActive, StatusInactive, StatusArchived}

// ListParams drives both the public catalog and the admin product table.
type ListParams struct {
	Q            string
	CategorySlug string
	CategoryID   int64
	MinPrice     int64 // poisha, 0 = no bound
	MaxPrice     int64
	InStock      bool
	OnSale       bool
	Featured     bool
	BestSeller   bool
	OfferID      int64  // products targeted by a campaign
	Sort         string // newest | price_asc | price_desc | popular | best_selling | trending | name
	Page         int
	PerPage      int

	// Admin-only filters. Public listings always force status=active.
	Admin  bool
	Status string
	Stock  string // in | low | out
}

type Page[T any] = paging.Page[T]

// ProductInput is the validated admin form for creating/updating a product.
type ProductInput struct {
	ID           int64
	CategoryID   *int64
	Name         string
	Slug         string
	SKU          string
	Brand        string
	Description  string
	Specs        []Spec
	RegularPrice int64
	SellingPrice int64
	Status       string
	IsFeatured   bool
	IsBestSeller bool
	InitialStock int // only used on create (default variant)
}
