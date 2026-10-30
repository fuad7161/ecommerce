package orders

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
)

var (
	ErrNotFound          = errors.New("order not found")
	ErrInvalidZone       = errors.New("please choose a valid delivery area")
	ErrEmptyCart         = errors.New("your cart is empty")
	ErrCartChanged       = errors.New("some items in your cart changed (price, stock or availability); please review your cart")
	ErrInvalidTransition = errors.New("this status change is not allowed")
	ErrCODDisabled       = errors.New("cash on delivery is currently unavailable")
)

// FieldErrors maps form field names to user-facing messages.
type FieldErrors map[string]string

func (f FieldErrors) Error() string {
	msgs := make([]string, 0, len(f))
	for k, v := range f {
		msgs = append(msgs, k+": "+v)
	}
	sort.Strings(msgs)
	return strings.Join(msgs, "; ")
}

const (
	StatusPending     = "pending"
	StatusConfirmed   = "confirmed"
	StatusProcessing  = "processing"
	StatusReadyToShip = "ready_to_ship"
	StatusShipped     = "shipped"
	StatusInTransit   = "in_transit"
	StatusDelivered   = "delivered"
	StatusCancelled   = "cancelled"
	StatusReturned    = "returned"
)

var Statuses = []string{
	StatusPending, StatusConfirmed, StatusProcessing, StatusReadyToShip,
	StatusShipped, StatusInTransit, StatusDelivered, StatusCancelled, StatusReturned,
}

var statusLabels = map[string]string{
	StatusPending:     "Pending confirmation",
	StatusConfirmed:   "Confirmed",
	StatusProcessing:  "Processing",
	StatusReadyToShip: "Ready for shipment",
	StatusShipped:     "Shipped",
	StatusInTransit:   "In transit",
	StatusDelivered:   "Delivered",
	StatusCancelled:   "Cancelled",
	StatusReturned:    "Returned",
}

func StatusLabel(s string) string {
	if l, ok := statusLabels[s]; ok {
		return l
	}
	return s
}

// transitions is the order state machine. Inventory side effects are applied in UpdateStatus.
var transitions = map[string][]string{
	StatusPending:     {StatusConfirmed, StatusCancelled},
	StatusConfirmed:   {StatusProcessing, StatusCancelled},
	StatusProcessing:  {StatusReadyToShip, StatusCancelled},
	StatusReadyToShip: {StatusShipped, StatusCancelled},
	StatusShipped:     {StatusInTransit, StatusDelivered, StatusReturned},
	StatusInTransit:   {StatusDelivered, StatusReturned},
	StatusDelivered:   {StatusReturned},
}

func NextStatuses(from string) []string { return transitions[from] }

func CanTransition(from, to string) bool { return slices.Contains(transitions[from], to) }

// TrackingSteps is the happy-path timeline shown to customers.
var TrackingSteps = []string{
	StatusPending, StatusConfirmed, StatusProcessing, StatusReadyToShip, StatusShipped, StatusInTransit, StatusDelivered,
}

type Item struct {
	ProductID    *int64
	ProductSlug  string
	ProductName  string
	VariantName  string
	SKU          string
	ImageKey     string
	OfferTitle   string
	RegularPrice int64
	UnitPrice    int64
	Quantity     int
	LineTotal    int64
}

type StatusChange struct {
	From      string
	To        string
	Note      string
	AdminName string
	CreatedAt time.Time
}

type Note struct {
	AdminName string
	Body      string
	CreatedAt time.Time
}

type Order struct {
	ID             int64
	Number         string
	CustomerID     int64
	CustomerName   string
	Phone          string
	ZoneName       string
	District       string
	Area           string
	Address        string
	DeliveryNotes  string
	PaymentMethod  string
	PaymentStatus  string
	Status         string
	Subtotal       int64
	Discount       int64
	DeliveryCharge int64
	Total          int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Items          []Item
	History        []StatusChange
	Notes          []Note
}

func (o *Order) StatusLabel() string    { return StatusLabel(o.Status) }
func (o *Order) NextStatuses() []string { return NextStatuses(o.Status) }

// StepReached reports whether the order has reached the given happy-path step.
func (o *Order) StepReached(step string) bool {
	cur := slices.Index(TrackingSteps, o.Status)
	return cur >= 0 && slices.Index(TrackingSteps, step) <= cur
}

// OrderRow is the compact form used in admin tables.
type OrderRow struct {
	ID           int64
	Number       string
	CustomerName string
	Phone        string
	District     string
	Status       string
	Payment      string
	ItemCount    int
	Total        int64
	CreatedAt    time.Time
}

func (o OrderRow) StatusLabel() string { return StatusLabel(o.Status) }

type CheckoutInput struct {
	Name           string
	Phone          string
	District       string
	Area           string
	Address        string
	Notes          string
	ZoneID         int64
	PaymentMethod  string
	Confirmed      bool
	IdempotencyKey string
	Items          []CartLine
}

type ListParams struct {
	Q       string
	Status  string
	Payment string
	From    time.Time
	To      time.Time // inclusive day
	Page    int
	PerPage int
}

type Customer struct {
	ID          int64
	Phone       string
	Name        string
	District    string
	Area        string
	Address     string
	Notes       string
	OrderCount  int
	TotalSpent  int64
	LastOrderAt *time.Time
	CreatedAt   time.Time
}
