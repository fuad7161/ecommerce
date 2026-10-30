package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/fuad71/ecommerce/internal/catalog"
	"github.com/fuad71/ecommerce/internal/inventory"
	"github.com/fuad71/ecommerce/internal/media"
	"github.com/fuad71/ecommerce/internal/money"
)

// ---------- products ----------

func (s *Server) adminProducts(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	lp := catalog.ListParams{
		Admin: true, Q: q.Get("q"), Status: q.Get("status"), Stock: q.Get("stock"),
		Sort: q.Get("sort"), PerPage: 20,
	}
	lp.Page, _ = strconv.Atoi(q.Get("page"))
	lp.CategoryID, _ = strconv.ParseInt(q.Get("category"), 10, 64)
	page, err := s.catalog.ListProducts(r.Context(), lp)
	if err != nil {
		return err
	}
	data := map[string]any{"Title": "Products", "Page": page, "F": lp, "Query": q}
	if isHTMX(r) && r.Header.Get("HX-Target") == "results" {
		s.render(w, r, http.StatusOK, "admin/products", "results", data)
		return nil
	}
	cats, err := s.catalog.Categories(r.Context(), false)
	if err != nil {
		return err
	}
	data["Categories"] = cats
	s.page(w, r, "admin/products", data)
	return nil
}

func (s *Server) adminProductNew(w http.ResponseWriter, r *http.Request) error {
	return s.renderProductForm(w, r, http.StatusOK, nil, map[string]string{"status": "draft"}, nil)
}

func (s *Server) adminProductEdit(w http.ResponseWriter, r *http.Request) error {
	p, err := s.catalog.ProductByID(r.Context(), idParam(r, "id"))
	if err != nil {
		return err
	}
	form := map[string]string{
		"name": p.Name, "slug": p.Slug, "sku": p.SKU, "brand": p.Brand, "description": p.Description,
		"specs": catalog.FormatSpecs(p.Specs), "regular_price": money.Plain(p.RegularPrice),
		"selling_price": money.Plain(p.SellingPrice), "status": p.Status,
	}
	if p.CategoryID != nil {
		form["category_id"] = strconv.FormatInt(*p.CategoryID, 10)
	}
	if p.IsFeatured {
		form["is_featured"] = "1"
	}
	if p.IsBestSeller {
		form["is_best_seller"] = "1"
	}
	return s.renderProductForm(w, r, http.StatusOK, p, form, nil)
}

func (s *Server) renderProductForm(w http.ResponseWriter, r *http.Request, status int, p *catalog.Product,
	form map[string]string, errs map[string]string) error {
	cats, err := s.catalog.Categories(r.Context(), false)
	if err != nil {
		return err
	}
	title := "New product"
	if p != nil {
		title = p.Name
	}
	if errs == nil {
		errs = map[string]string{}
	}
	s.render(w, r, status, "admin/product_form", "layout", map[string]any{
		"Title": title, "Product": p, "Form": form, "Errors": errs, "Categories": cats,
		"Statuses": catalog.ProductStatuses,
	})
	return nil
}

func (s *Server) adminProductSave(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	f := r.PostForm
	in := &catalog.ProductInput{
		ID: idParam(r, "id"), Name: f.Get("name"), Slug: f.Get("slug"), SKU: f.Get("sku"), Brand: f.Get("brand"),
		Description: strings.TrimSpace(f.Get("description")), Specs: catalog.ParseSpecs(f.Get("specs")),
		Status: f.Get("status"), IsFeatured: f.Get("is_featured") == "1", IsBestSeller: f.Get("is_best_seller") == "1",
		InitialStock: formInt(r, "initial_stock"),
	}
	if cid := formInt64(r, "category_id"); cid > 0 {
		in.CategoryID = &cid
	}
	errs := map[string]string{}
	var err error
	if in.RegularPrice, err = money.Parse(f.Get("regular_price")); err != nil {
		errs["regular_price"] = "Enter a valid price in taka"
	}
	if in.SellingPrice, err = money.Parse(f.Get("selling_price")); err != nil {
		errs["selling_price"] = "Enter a valid price in taka"
	}
	if len(errs) == 0 {
		err = s.catalog.SaveProduct(r.Context(), adminFrom(r).ID, in)
		var verr catalog.ValidationError
		switch {
		case errors.As(err, &verr):
			errs = verr
		case err != nil:
			return err
		}
	}
	if len(errs) > 0 {
		form := map[string]string{}
		for k := range f {
			form[k] = f.Get(k)
		}
		var p *catalog.Product
		if in.ID > 0 {
			if p, err = s.catalog.ProductByID(r.Context(), in.ID); err != nil {
				return err
			}
		}
		return s.renderProductForm(w, r, http.StatusUnprocessableEntity, p, form, errs)
	}

	action := "product.update"
	if idParam(r, "id") == 0 {
		action = "product.create"
	}
	s.audit(r, action, "product", in.ID, map[string]any{"name": in.Name, "sku": in.SKU, "status": in.Status,
		"regular_price": in.RegularPrice, "selling_price": in.SellingPrice})
	s.flash(r, "Product saved.")
	redirect(w, r, fmt.Sprintf("/admin/products/%d", in.ID))
	return nil
}

func (s *Server) adminProductDelete(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	if err := s.catalog.DeleteProduct(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "product.delete", "product", id, nil)
	s.flash(r, "Product deleted.")
	redirect(w, r, "/admin/products")
	return nil
}

func (s *Server) adminProductImages(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	if _, err := s.catalog.ProductByID(r.Context(), id); err != nil {
		return err
	}
	r.Body = http.MaxBytesReader(w, r.Body, 10*s.cfg.MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		s.flash(r, "Upload failed: files are too large.")
		redirect(w, r, fmt.Sprintf("/admin/products/%d#images", id))
		return nil
	}
	files := r.MultipartForm.File["images"]
	var uploaded int
	var problems []string
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			return err
		}
		key, err := s.media.Upload(r.Context(), fmt.Sprintf("products/%d", id), f)
		f.Close()
		if media.IsUserError(err) {
			problems = append(problems, fh.Filename+": "+err.Error())
			continue
		}
		if err != nil {
			return err
		}
		if err := s.catalog.AddImage(r.Context(), id, key); err != nil {
			return err
		}
		uploaded++
	}
	s.audit(r, "product.images.upload", "product", id, map[string]any{"count": uploaded})
	msg := fmt.Sprintf("%d image(s) uploaded.", uploaded)
	if len(problems) > 0 {
		msg += " Skipped: " + strings.Join(problems, "; ")
	}
	s.flash(r, msg)
	redirect(w, r, fmt.Sprintf("/admin/products/%d#images", id))
	return nil
}

func (s *Server) adminProductImageDelete(w http.ResponseWriter, r *http.Request) error {
	id, imageID := idParam(r, "id"), idParam(r, "imageID")
	key, err := s.catalog.DeleteImage(r.Context(), id, imageID)
	if err != nil {
		return err
	}
	if err := s.media.Delete(r.Context(), key); err != nil {
		slog.WarnContext(r.Context(), "delete image object", "key", key, "err", err)
	}
	s.audit(r, "product.images.delete", "product", id, map[string]any{"key": key})
	redirect(w, r, fmt.Sprintf("/admin/products/%d#images", id))
	return nil
}

func (s *Server) adminProductImagePrimary(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	if err := s.catalog.MakePrimaryImage(r.Context(), id, idParam(r, "imageID")); err != nil {
		return err
	}
	redirect(w, r, fmt.Sprintf("/admin/products/%d#images", id))
	return nil
}

func (s *Server) adminVariantAdd(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	err := s.catalog.AddVariant(r.Context(), adminFrom(r).ID, id, r.FormValue("name"), r.FormValue("sku"),
		formInt(r, "stock"), max(formInt(r, "threshold"), 0))
	var verr catalog.ValidationError
	switch {
	case errors.As(err, &verr):
		s.flash(r, "Variant not added: "+verr.Error())
	case err != nil:
		return err
	default:
		s.audit(r, "variant.create", "product", id, map[string]any{"name": r.FormValue("name"), "sku": r.FormValue("sku")})
		s.flash(r, "Variant added.")
	}
	redirect(w, r, fmt.Sprintf("/admin/products/%d#variants", id))
	return nil
}

func (s *Server) adminVariantUpdate(w http.ResponseWriter, r *http.Request) error {
	v, err := s.catalog.Variant(r.Context(), idParam(r, "id"))
	if err != nil {
		return err
	}
	if err := s.catalog.UpdateVariant(r.Context(), v.ID, r.FormValue("name"), formInt(r, "threshold"), r.FormValue("active") == "1"); err != nil {
		return err
	}
	s.audit(r, "variant.update", "variant", v.ID, map[string]any{"name": r.FormValue("name"), "active": r.FormValue("active") == "1"})
	s.flash(r, "Variant updated.")
	redirect(w, r, fmt.Sprintf("/admin/products/%d#variants", v.ProductID))
	return nil
}

func (s *Server) adminVariantAdjust(w http.ResponseWriter, r *http.Request) error {
	v, err := s.catalog.Variant(r.Context(), idParam(r, "id"))
	if err != nil {
		return err
	}
	delta, note := formInt(r, "delta"), strings.TrimSpace(r.FormValue("note"))
	err = s.catalog.AdjustStock(r.Context(), adminFrom(r).ID, v.ID, delta, note)
	var verr catalog.ValidationError
	switch {
	case errors.As(err, &verr):
		s.flash(r, "Stock not changed: "+verr["delta"])
	case err != nil:
		return err
	default:
		s.audit(r, "inventory.adjust", "variant", v.ID, map[string]any{"sku": v.SKU, "delta": delta, "note": note})
		s.flash(r, fmt.Sprintf("Stock for %s adjusted by %+d.", v.SKU, delta))
	}
	redirect(w, r, fmt.Sprintf("/admin/products/%d#variants", v.ProductID))
	return nil
}

func (s *Server) adminVariantHistory(w http.ResponseWriter, r *http.Request) error {
	moves, err := inventory.History(r.Context(), s.pool, idParam(r, "id"), 50)
	if err != nil {
		return err
	}
	s.render(w, r, http.StatusOK, "admin/product_form", "stock_history", map[string]any{"Moves": moves})
	return nil
}

// ---------- categories ----------

func (s *Server) adminCategories(w http.ResponseWriter, r *http.Request) error {
	cats, err := s.catalog.Categories(r.Context(), false)
	if err != nil {
		return err
	}
	s.page(w, r, "admin/categories", map[string]any{"Title": "Categories", "Categories": cats})
	return nil
}

func (s *Server) adminCategoryForm(w http.ResponseWriter, r *http.Request) error {
	c := &catalog.Category{Active: true}
	if id := idParam(r, "id"); id > 0 {
		var err error
		if c, err = s.catalog.CategoryByID(r.Context(), id); err != nil {
			return err
		}
	}
	return s.renderCategoryForm(w, r, http.StatusOK, c, nil)
}

func (s *Server) renderCategoryForm(w http.ResponseWriter, r *http.Request, status int, c *catalog.Category, errs map[string]string) error {
	all, err := s.catalog.Categories(r.Context(), false)
	if err != nil {
		return err
	}
	var parents []catalog.Category
	for _, p := range all {
		if p.ParentID == nil && p.ID != c.ID {
			parents = append(parents, p)
		}
	}
	title := "New category"
	if c.ID > 0 {
		title = c.Name
	}
	if errs == nil {
		errs = map[string]string{}
	}
	s.render(w, r, status, "admin/category_form", "layout", map[string]any{
		"Title": title, "Category": c, "Parents": parents, "Errors": errs,
	})
	return nil
}

func (s *Server) adminCategorySave(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return err
	}
	c := &catalog.Category{ID: idParam(r, "id")}
	if c.ID > 0 {
		existing, err := s.catalog.CategoryByID(r.Context(), c.ID)
		if err != nil {
			return err
		}
		c.ImageKey = existing.ImageKey
	}
	c.Name, c.Slug, c.Description = r.FormValue("name"), r.FormValue("slug"), strings.TrimSpace(r.FormValue("description"))
	c.SortOrder, c.Active = formInt(r, "sort_order"), r.FormValue("active") == "1"
	if pid := formInt64(r, "parent_id"); pid > 0 {
		c.ParentID = &pid
	}
	if f, fh, err := r.FormFile("image"); err == nil {
		key, err := s.media.Upload(r.Context(), "categories", f)
		f.Close()
		if media.IsUserError(err) {
			return s.renderCategoryForm(w, r, http.StatusUnprocessableEntity, c, map[string]string{"image": fh.Filename + ": " + err.Error()})
		}
		if err != nil {
			return err
		}
		c.ImageKey = key
	}
	err := s.catalog.SaveCategory(r.Context(), c)
	var verr catalog.ValidationError
	if errors.As(err, &verr) {
		return s.renderCategoryForm(w, r, http.StatusUnprocessableEntity, c, verr)
	}
	if err != nil {
		return err
	}
	s.audit(r, "category.save", "category", c.ID, map[string]any{"name": c.Name, "active": c.Active})
	s.flash(r, "Category saved.")
	redirect(w, r, "/admin/categories")
	return nil
}

func (s *Server) adminCategoryDelete(w http.ResponseWriter, r *http.Request) error {
	id := idParam(r, "id")
	if err := s.catalog.DeleteCategory(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "category.delete", "category", id, nil)
	s.flash(r, "Category deleted. Its products are now uncategorised.")
	redirect(w, r, "/admin/categories")
	return nil
}
