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
		"store/offers", "store/apidocs", "admin/offers", "admin/offer_form", "admin/homepage", "admin/account", "admin/admins",
		"store/account_login", "store/account_register", "store/account_orders", "store/account_order", "store/account_profile"} {
		if _, ok := r.sets[want]; !ok {
			t.Errorf("missing template set %s", want)
		}
	}
}

func TestSafeNextRejectsOffsiteRedirects(t *testing.T) {
	for in, want := range map[string]string{
		"/checkout":                "/checkout",
		"/order/261030-1?t=abc":    "/order/261030-1?t=abc",
		"":                         "/account",
		"https://evil.example":     "/account",
		"//evil.example/x":         "/account",
		"/\\evil.example":          "/account",
		"/x\r\nSet-Cookie: a=b":    "/account",
		"/account/login?next=/foo": "/account",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
