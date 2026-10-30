package offers

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	base := func() *Offer {
		return &Offer{Title: "Eid Sale", DiscountType: TypePercent, DiscountValue: 20,
			StartsAt: now, EndsAt: now.Add(72 * time.Hour), ProductIDs: []int64{1}}
	}

	if v := Validate(base(), true, now); len(v) != 0 {
		t.Fatalf("valid offer rejected: %v", v)
	}
	o := base()
	Validate(o, true, now)
	if o.Slug != "eid-sale" {
		t.Fatalf("slug = %q", o.Slug)
	}

	cases := map[string]func(o *Offer){
		"discount_value": func(o *Offer) { o.DiscountValue = 95 },
		"discount_type":  func(o *Offer) { o.DiscountType = "bogo" },
		"dates":          func(o *Offer) { o.EndsAt = o.StartsAt.Add(-time.Hour) },
		"targets":        func(o *Offer) { o.ProductIDs = nil },
		"title":          func(o *Offer) { o.Title = "  " },
	}
	for field, mutate := range cases {
		o := base()
		mutate(o)
		if v := Validate(o, true, now); v[field] == "" {
			t.Errorf("expected %s error, got %v", field, v)
		}
	}

	expired := base()
	expired.StartsAt, expired.EndsAt = now.Add(-48*time.Hour), now.Add(-time.Hour)
	if v := Validate(expired, true, now); v["dates"] == "" {
		t.Error("creating an already-expired campaign must be rejected")
	}
	if v := Validate(expired, false, now); len(v) != 0 {
		t.Errorf("editing a past campaign should be allowed: %v", v)
	}

	fixed := base()
	fixed.DiscountType, fixed.DiscountValue = TypeFixed, 0
	if v := Validate(fixed, true, now); v["discount_value"] == "" {
		t.Error("fixed discount of zero must be rejected")
	}
}

func TestState(t *testing.T) {
	now := time.Now()
	o := &Offer{Active: true, StartsAt: now.Add(time.Hour), EndsAt: now.Add(2 * time.Hour)}
	if o.State(now) != "scheduled" {
		t.Fatal(o.State(now))
	}
	o.StartsAt = now.Add(-time.Hour)
	if o.State(now) != "running" {
		t.Fatal(o.State(now))
	}
	o.EndsAt = now
	if o.State(now) != "expired" {
		t.Fatal(o.State(now))
	}
	o.Active = false
	if o.State(now) != "inactive" {
		t.Fatal(o.State(now))
	}
}
