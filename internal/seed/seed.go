// Package seed loads demo data for local development.
package seed

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuad71/ecommerce/internal/auth"
	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/media"
	"github.com/fuad71/ecommerce/internal/offers"
)

type product struct {
	name, category, brand, desc string
	regular, selling            int64 // taka
	stock                       int
	featured, bestSeller        bool
	specs                       []catalog.Spec
	variants                    []string // named variants; stock is split between them
}

var categories = []struct {
	name     string
	children []string
}{
	{"Electronics", []string{"Mobile Phones", "Audio", "Accessories"}},
	{"Fashion", []string{"Men", "Women"}},
	{"Home & Kitchen", nil},
	{"Beauty & Care", nil},
}

var products = []product{
	{"Redmi Note 13 (8/128GB)", "Mobile Phones", "Xiaomi", "6.67\" AMOLED display, 108MP camera and 5000mAh battery with 33W fast charging.", 24999, 22499, 25, true, true,
		specs("Display", "6.67\" AMOLED 120Hz", "RAM / Storage", "8GB / 128GB", "Battery", "5000mAh"), []string{"Midnight Black", "Ocean Blue"}},
	{"Samsung Galaxy A15", "Mobile Phones", "Samsung", "Super AMOLED display, triple camera and long-lasting battery.", 21999, 20999, 12, false, false,
		specs("Display", "6.5\" Super AMOLED", "Battery", "5000mAh"), nil},
	{"Wireless Earbuds Pro", "Audio", "SoundCore", "Active noise cancelling earbuds with 30-hour battery life and IPX5 water resistance.", 3500, 2490, 40, true, true,
		specs("Battery", "Up to 30 hours", "Bluetooth", "5.3", "Water resistance", "IPX5"), nil},
	{"Bluetooth Neckband", "Audio", "QCY", "Lightweight magnetic neckband with deep bass.", 1500, 1150, 60, false, true, nil, nil},
	{"Portable Bluetooth Speaker", "Audio", "JBL", "Waterproof portable speaker with punchy bass and 12 hours of playtime.", 6500, 5800, 8, true, false,
		specs("Playtime", "12 hours", "Waterproof", "IP67"), nil},
	{"20W USB-C Fast Charger", "Accessories", "Anker", "Compact 20W PD charger for phones and tablets.", 1200, 950, 100, false, false, nil, nil},
	{"10000mAh Power Bank", "Accessories", "Xiaomi", "Slim power bank with two-way 22.5W fast charging.", 2200, 1899, 3, false, true, nil, nil},
	{"Smart Watch S8", "Accessories", "Haylou", "1.85\" display, heart-rate and SpO2 monitoring, 10-day battery.", 4500, 3290, 0, true, false, nil, nil},
	{"Premium Cotton Panjabi", "Men", "Aarong Style", "Soft cotton panjabi with embroidered collar, perfect for Eid and festivals.", 2800, 2200, 30, true, true,
		specs("Fabric", "100% cotton", "Fit", "Regular"), []string{"M", "L", "XL"}},
	{"Classic Polo T-Shirt", "Men", "Yellow", "Breathable pique cotton polo shirt.", 1250, 1250, 45, false, false, nil, []string{"M", "L", "XL"}},
	{"Slim Fit Denim Jeans", "Men", "Easy", "Stretch denim with a modern slim fit.", 2400, 1990, 20, false, false, nil, []string{"30", "32", "34"}},
	{"Printed Cotton Three-Piece", "Women", "Kay Kraft", "Unstitched printed cotton three-piece with matching dupatta.", 3800, 2990, 18, true, true, nil, nil},
	{"Jamdani Saree", "Women", "Tangail Weavers", "Handwoven Jamdani saree with traditional motifs.", 9500, 8500, 5, true, false, nil, nil},
	{"Non-Stick Fry Pan 26cm", "Home & Kitchen", "Kiam", "Durable non-stick coating, suitable for gas and induction.", 1650, 1350, 35, false, true, nil, nil},
	{"Electric Kettle 1.8L", "Home & Kitchen", "Walton", "Stainless steel kettle with auto shut-off.", 1990, 1690, 22, false, false, nil, nil},
	{"Rice Cooker 1.8L", "Home & Kitchen", "Miyako", "Keeps rice warm for hours, with steaming tray.", 3200, 2850, 14, true, false, nil, nil},
	{"Vitamin C Face Serum", "Beauty & Care", "The Ordinary", "Brightening serum for an even skin tone.", 1800, 1450, 50, false, true, nil, nil},
	{"Herbal Hair Oil 200ml", "Beauty & Care", "Parachute", "Nourishing hair oil with amla and coconut.", 450, 420, 80, false, false, nil, nil},
}

func specs(kv ...string) []catalog.Spec {
	var out []catalog.Spec
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, catalog.Spec{Name: kv[i], Value: kv[i+1]})
	}
	return out
}

func Run(ctx context.Context, pool *pgxpool.Pool, ms *media.Store) error {
	authSvc := auth.NewService(pool)
	if n, err := authSvc.Count(ctx); err != nil {
		return err
	} else if n == 0 {
		email, password := os.Getenv("SEED_ADMIN_EMAIL"), os.Getenv("SEED_ADMIN_PASSWORD")
		if email == "" || password == "" {
			return fmt.Errorf("set SEED_ADMIN_EMAIL and SEED_ADMIN_PASSWORD to create the first admin")
		}
		if _, err := authSvc.Create(ctx, email, "Store Owner", password, auth.RoleAdmin); err != nil {
			return err
		}
		slog.Info("seed: admin created", "email", email)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM products`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		slog.Info("seed: catalog already has products, skipping")
		return seedOffer(ctx, pool)
	}

	cat := catalog.NewService(pool)
	catIDs := map[string]int64{}
	for i, c := range categories {
		parent := &catalog.Category{Name: c.name, SortOrder: i, Active: true}
		if err := cat.SaveCategory(ctx, parent); err != nil {
			return err
		}
		catIDs[c.name] = parent.ID
		for j, ch := range c.children {
			child := &catalog.Category{Name: ch, ParentID: &parent.ID, SortOrder: j, Active: true}
			if err := cat.SaveCategory(ctx, child); err != nil {
				return err
			}
			catIDs[ch] = child.ID
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	for i, p := range products {
		cid := catIDs[p.category]
		in := &catalog.ProductInput{
			CategoryID: &cid, Name: p.name, SKU: fmt.Sprintf("BZ-%04d", 1001+i), Brand: p.brand, Description: p.desc,
			Specs: p.specs, RegularPrice: p.regular * 100, SellingPrice: p.selling * 100, Status: catalog.StatusActive,
			IsFeatured: p.featured, IsBestSeller: p.bestSeller,
		}
		if len(p.variants) == 0 {
			in.InitialStock = p.stock
		}
		if err := cat.SaveProduct(ctx, 0, in); err != nil {
			return fmt.Errorf("seed product %s: %w", p.name, err)
		}
		if len(p.variants) > 0 {
			// Replace the default variant with named options.
			if _, err := pool.Exec(ctx, `UPDATE product_variants SET active = false WHERE product_id = $1`, in.ID); err != nil {
				return err
			}
			for j, v := range p.variants {
				sku := fmt.Sprintf("%s-%d", in.SKU, j+1)
				if err := cat.AddVariant(ctx, 0, in.ID, v, sku, p.stock/len(p.variants), 3); err != nil {
					return err
				}
			}
		}
		key, err := ms.Upload(ctx, fmt.Sprintf("products/%d", in.ID), bytes.NewReader(productImage(client, in.Slug)))
		if err != nil {
			return err
		}
		if err := cat.AddImage(ctx, in.ID, key); err != nil {
			return err
		}
		slog.Info("seed: product", "name", p.name)
	}
	slog.Info("seed: done", "products", len(products))
	return seedOffer(ctx, pool)
}

// seedOffer adds a demo campaign (15% off Audio for two weeks) when there are no campaigns.
func seedOffer(ctx context.Context, pool *pgxpool.Pool) error {
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM offers`).Scan(&n); err != nil || n > 0 {
		return err
	}
	var catID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM categories WHERE slug = 'audio' AND deleted_at IS NULL`).Scan(&catID); err != nil {
		return nil // demo category not present
	}
	o := &offers.Offer{Title: "Audio Fest", Description: "15% off headphones, earbuds and speakers for two weeks.",
		DiscountType: offers.TypePercent, DiscountValue: 15, StartsAt: time.Now().Add(-time.Hour),
		EndsAt: time.Now().AddDate(0, 0, 14), Active: true, ShowOnHome: true, CategoryIDs: []int64{catID}}
	if err := offers.NewService(pool).Save(ctx, o); err != nil {
		return err
	}
	slog.Info("seed: demo campaign created", "offer", o.Title)
	return nil
}

// productImage downloads a free placeholder photo, or draws a coloured tile when offline.
func productImage(client *http.Client, seed string) []byte {
	if os.Getenv("SEED_OFFLINE") != "true" {
		resp, err := client.Get("https://picsum.photos/seed/" + seed + "/800/800.jpg")
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)); err == nil && len(b) > 0 {
					return b
				}
			}
		}
	}
	h := fnv.New32a()
	h.Write([]byte(seed))
	sum := h.Sum32()
	base := color.RGBA{uint8(sum), uint8(sum >> 8), uint8(sum >> 16), 255}
	img := image.NewRGBA(image.Rect(0, 0, 600, 600))
	for y := range 600 {
		for x := range 600 {
			c := base
			if (x/60+y/60)%2 == 0 {
				c.R, c.G, c.B = c.R/2+100, c.G/2+100, c.B/2+100
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
