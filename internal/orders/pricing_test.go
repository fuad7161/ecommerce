package orders

import "testing"

func TestComputeTotals(t *testing.T) {
	lines := func() []QuoteLine {
		return []QuoteLine{
			{SKU: "A", RegularPrice: 100000, UnitPrice: 80000, Qty: 2}, // ৳1000 -> ৳800 x2
			{SKU: "B", RegularPrice: 50000, UnitPrice: 50000, Qty: 1},  // ৳500 x1
		}
	}

	t.Run("discount and delivery", func(t *testing.T) {
		q := &Quote{Lines: lines()}
		computeTotals(q, 7000, 0)
		if q.Subtotal != 250000 || q.Discount != 40000 || q.DeliveryCharge != 7000 || q.Total != 217000 {
			t.Fatalf("got subtotal=%d discount=%d delivery=%d total=%d", q.Subtotal, q.Discount, q.DeliveryCharge, q.Total)
		}
		if q.Lines[0].LineTotal != 160000 || q.ItemCount != 3 {
			t.Fatalf("line total %d, items %d", q.Lines[0].LineTotal, q.ItemCount)
		}
	})

	t.Run("free delivery threshold uses discounted amount", func(t *testing.T) {
		q := &Quote{Lines: lines()}
		computeTotals(q, 7000, 210000) // net 210000 >= threshold
		if !q.FreeDelivery || q.DeliveryCharge != 0 || q.Total != 210000 {
			t.Fatalf("expected free delivery, got %+v", q)
		}
		q = &Quote{Lines: lines()}
		computeTotals(q, 7000, 210001)
		if q.FreeDelivery || q.DeliveryCharge != 7000 {
			t.Fatalf("expected paid delivery, got %+v", q)
		}
	})

	t.Run("empty cart has no delivery charge", func(t *testing.T) {
		q := &Quote{}
		computeTotals(q, 7000, 0)
		if q.Total != 0 || q.DeliveryCharge != 0 {
			t.Fatalf("got %+v", q)
		}
	})

	t.Run("problem lines flag the quote", func(t *testing.T) {
		q := &Quote{Lines: []QuoteLine{{SKU: "A", RegularPrice: 100, UnitPrice: 100, Qty: 0, Problem: "Out of stock"}}}
		computeTotals(q, 0, 0)
		if !q.HasProblems || q.Total != 0 {
			t.Fatalf("got %+v", q)
		}
	})
}

func TestMergeCart(t *testing.T) {
	got := mergeCart([]CartLine{{"a", 1}, {" A ", 2}, {"b", 0}, {"", 3}, {"c", 999}})
	if len(got) != 2 || got[0] != (CartLine{"A", 3}) || got[1] != (CartLine{"C", MaxQtyPerLine}) {
		t.Fatalf("got %+v", got)
	}
}

func TestTransitions(t *testing.T) {
	if !CanTransition(StatusPending, StatusConfirmed) || !CanTransition(StatusShipped, StatusReturned) {
		t.Fatal("expected allowed transitions")
	}
	if CanTransition(StatusDelivered, StatusCancelled) || CanTransition(StatusCancelled, StatusConfirmed) ||
		CanTransition(StatusPending, StatusShipped) {
		t.Fatal("unexpected allowed transition")
	}
}
