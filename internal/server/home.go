package server

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/fuad71/ecommerce/internal/cache"
	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/offers"
)

// homeSectionDef describes a homepage block the admin can enable and reorder.
type homeSectionDef struct {
	Key   string
	Title string
	Link  string
	List  *catalog.ListParams // nil for non-product sections
}

var homeSectionDefs = []homeSectionDef{
	{Key: "campaigns", Title: "Running campaigns", Link: "/offers"},
	{Key: "categories", Title: "Shop by category", Link: "/products"},
	{Key: "offers", Title: "Special offers", Link: "/products?on_sale=1", List: &catalog.ListParams{OnSale: true, Sort: "popular", PerPage: 8}},
	{Key: "featured", Title: "Featured products", Link: "/products?sort=popular", List: &catalog.ListParams{Featured: true, Sort: "popular", PerPage: 8}},
	{Key: "trending", Title: "Trending now", Link: "/products?sort=trending", List: &catalog.ListParams{Sort: "trending", PerPage: 8}},
	{Key: "best_sellers", Title: "Best sellers", Link: "/products?sort=best_selling", List: &catalog.ListParams{BestSeller: true, Sort: "best_selling", PerPage: 8}},
	{Key: "new_arrivals", Title: "New arrivals", Link: "/products?sort=newest", List: &catalog.ListParams{Sort: "newest", PerPage: 8}},
}

func findSection(key string) (homeSectionDef, bool) {
	i := slices.IndexFunc(homeSectionDefs, func(d homeSectionDef) bool { return d.Key == key })
	if i < 0 {
		return homeSectionDef{}, false
	}
	return homeSectionDefs[i], true
}

// enabledSections parses the "home_sections" setting (ordered, comma separated).
func enabledSections(setting string) []string {
	var out []string
	for _, k := range strings.Split(setting, ",") {
		k = strings.TrimSpace(k)
		if _, ok := findSection(k); ok && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// HomeSection is one rendered homepage block (JSON-cacheable).
type HomeSection struct {
	Key        string
	Title      string
	Link       string
	Products   []catalog.ProductCard
	Categories []catalog.Category
	Campaigns  []offers.Offer
}

func (s *Server) homeSections(ctx context.Context) ([]HomeSection, error) {
	st, err := s.settings.All(ctx)
	if err != nil {
		return nil, err
	}
	keys := enabledSections(st.Get("home_sections"))
	return cache.Fetch(ctx, s.cache, "home:"+strings.Join(keys, ","), func() ([]HomeSection, error) {
		var out []HomeSection
		for _, k := range keys {
			def, _ := findSection(k)
			sec := HomeSection{Key: def.Key, Title: def.Title, Link: def.Link}
			switch {
			case def.Key == "campaigns":
				if sec.Campaigns, err = s.offers.Running(ctx, true); err != nil {
					return nil, err
				}
			case def.Key == "categories":
				if sec.Categories, err = s.catalog.CategoryTree(ctx); err != nil {
					return nil, err
				}
			case def.List != nil:
				page, err := s.catalog.ListProducts(ctx, *def.List)
				if err != nil {
					return nil, err
				}
				sec.Products = page.Items
			}
			out = append(out, sec)
		}
		return out, nil
	})
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) error {
	sections, err := s.homeSections(r.Context())
	if err != nil {
		return err
	}
	// The hero collage uses the first product section that has items.
	var hero []catalog.ProductCard
	for _, sec := range sections {
		if len(sec.Products) >= 4 {
			hero = sec.Products[:4]
			break
		}
	}
	s.page(w, r, "store/home", map[string]any{"Sections": sections, "Hero": hero})
	return nil
}

func (s *Server) offerList(w http.ResponseWriter, r *http.Request) error {
	list, err := s.offers.Running(r.Context(), false)
	if err != nil {
		return err
	}
	s.page(w, r, "store/offers", map[string]any{"Title": "Offers & campaigns", "Offers": list})
	return nil
}
