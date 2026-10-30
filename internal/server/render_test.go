package server

import "testing"

// Every page template must parse together with its layout and partials.
func TestTemplatesParse(t *testing.T) {
	r, err := newRenderer()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"store/home", "store/products", "store/product", "store/cart", "store/checkout",
		"store/order", "store/track", "store/error", "admin/login", "admin/dashboard", "admin/products",
		"admin/product_form", "admin/orders", "admin/order", "admin/invoice", "admin/settings",
		"store/offers", "store/apidocs", "admin/offers", "admin/offer_form", "admin/homepage", "admin/account", "admin/admins"} {
		if _, ok := r.sets[want]; !ok {
			t.Errorf("missing template set %s", want)
		}
	}
}
