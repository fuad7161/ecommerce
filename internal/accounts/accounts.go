// Package accounts manages optional customer accounts (email + password).
//
// An account is a convenience layer on top of guest checkout: it remembers delivery
// details and collects the customer's orders in one place. It never grants access to an
// order by phone number alone, because phone numbers are not verified (no SMS).
package accounts

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/fuad71/ecommerce/internal/db"
	"github.com/fuad71/ecommerce/internal/phone"
)

const (
	MinPassword = 8
	// bcrypt ignores bytes past 72; reject longer passwords instead of silently truncating.
	maxPassword = 72
	bcryptCost  = 12
)

var (
	ErrInvalidCredentials = errors.New("incorrect email or password")
	ErrNotFound           = errors.New("account not found")
)

// FieldErrors maps form field names to user-facing messages.
type FieldErrors map[string]string

func (f FieldErrors) Error() string { return "invalid account details" }

type Account struct {
	ID           int64
	Email        string
	Name         string
	Phone        string
	District     string
	Area         string
	Address      string
	SessionEpoch int
	LastLoginAt  *time.Time
	CreatedAt    time.Time
}

// FirstName is used for the greeting in the header.
func (a *Account) FirstName() string {
	if f := strings.Fields(a.Name); len(f) > 0 {
		return f[0]
	}
	return a.Name
}

type Service struct{ pool *pgxpool.Pool }

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// dummyHash keeps login timing similar for unknown emails, so emails can't be probed.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equaliser-password"), bcryptCost)

const cols = `id, email, name, phone, district, area, address, session_epoch, last_login_at, created_at`

func scan(row interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Email, &a.Name, &a.Phone, &a.District, &a.Area, &a.Address,
		&a.SessionEpoch, &a.LastLoginAt, &a.CreatedAt)
	if db.IsNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func normalizeEmail(email string) (string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 || !strings.Contains(email[strings.LastIndex(email, "@"):], ".") {
		return email, false
	}
	return email, true
}

func cleanName(name string) (string, bool) {
	name = strings.Join(strings.Fields(name), " ")
	n := utf8.RuneCountInString(name)
	return name, n >= 2 && n <= 80
}

func checkPassword(fe FieldErrors, field, pw string) {
	switch {
	case len(pw) < MinPassword:
		fe[field] = "Password must be at least 8 characters"
	case len(pw) > maxPassword:
		fe[field] = "Password is too long (72 characters max)"
	}
}

type RegisterInput struct {
	Name     string
	Email    string
	Phone    string // optional; only used to pre-fill checkout
	Password string
}

// Register creates an account. Validation problems come back as FieldErrors.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*Account, error) {
	fe := FieldErrors{}
	var ok bool
	if in.Name, ok = cleanName(in.Name); !ok {
		fe["name"] = "Please enter your full name (2–80 characters)"
	}
	if in.Email, ok = normalizeEmail(in.Email); !ok {
		fe["email"] = "Please enter a valid email address"
	}
	if strings.TrimSpace(in.Phone) != "" {
		p, err := phone.Normalize(in.Phone)
		if err != nil {
			fe["phone"] = err.Error()
		}
		in.Phone = p
	}
	checkPassword(fe, "password", in.Password)
	if len(fe) > 0 {
		return nil, fe
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcryptCost)
	if err != nil {
		return nil, err
	}
	a, err := scan(s.pool.QueryRow(ctx, `INSERT INTO accounts (email, name, phone, password_hash, last_login_at)
		VALUES ($1, $2, $3, $4, now()) RETURNING `+cols, in.Email, in.Name, in.Phone, hash))
	if db.IsUniqueViolation(err, "accounts_email_uq") {
		return nil, FieldErrors{"email": "An account with this email already exists. Try logging in instead."}
	}
	return a, err
}

func (s *Service) Authenticate(ctx context.Context, email, password string) (*Account, error) {
	email, _ = normalizeEmail(email)
	var hash string
	var a Account
	err := s.pool.QueryRow(ctx, `SELECT `+cols+`, password_hash FROM accounts WHERE lower(email) = $1`, email).
		Scan(&a.ID, &a.Email, &a.Name, &a.Phone, &a.District, &a.Area, &a.Address,
			&a.SessionEpoch, &a.LastLoginAt, &a.CreatedAt, &hash)
	if db.IsNoRows(err) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrInvalidCredentials
	}
	_, _ = s.pool.Exec(ctx, `UPDATE accounts SET last_login_at = now() WHERE id = $1`, a.ID)
	return &a, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Account, error) {
	return scan(s.pool.QueryRow(ctx, `SELECT `+cols+` FROM accounts WHERE id = $1`, id))
}

type ProfileInput struct {
	Name     string
	Phone    string
	District string
	Area     string
	Address  string
}

// UpdateProfile saves the name and default delivery details. All but the name are optional.
func (s *Service) UpdateProfile(ctx context.Context, id int64, in ProfileInput) error {
	fe := FieldErrors{}
	var ok bool
	if in.Name, ok = cleanName(in.Name); !ok {
		fe["name"] = "Please enter your full name (2–80 characters)"
	}
	if strings.TrimSpace(in.Phone) != "" {
		p, err := phone.Normalize(in.Phone)
		if err != nil {
			fe["phone"] = err.Error()
		}
		in.Phone = p
	}
	in.District, in.Area, in.Address = strings.TrimSpace(in.District), strings.TrimSpace(in.Area), strings.TrimSpace(in.Address)
	if utf8.RuneCountInString(in.District) > 80 {
		fe["district"] = "District is too long"
	}
	if utf8.RuneCountInString(in.Area) > 80 {
		fe["area"] = "Area is too long"
	}
	if utf8.RuneCountInString(in.Address) > 300 {
		fe["address"] = "Address is too long"
	}
	if len(fe) > 0 {
		return fe
	}
	_, err := s.pool.Exec(ctx, `UPDATE accounts SET name = $2, phone = $3, district = $4, area = $5, address = $6,
		updated_at = now() WHERE id = $1`, id, in.Name, in.Phone, in.District, in.Area, in.Address)
	return err
}

// ChangePassword verifies the current password, stores the new one and bumps the session
// epoch so every other logged-in device is signed out. It returns the new epoch.
func (s *Service) ChangePassword(ctx context.Context, id int64, current, next string) (int, error) {
	var hash string
	if err := s.pool.QueryRow(ctx, `SELECT password_hash FROM accounts WHERE id = $1`, id).Scan(&hash); err != nil {
		return 0, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
		return 0, FieldErrors{"current_password": "Your current password is incorrect"}
	}
	fe := FieldErrors{}
	checkPassword(fe, "new_password", next)
	if len(fe) > 0 {
		return 0, fe
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(next), bcryptCost)
	if err != nil {
		return 0, err
	}
	var epoch int
	err = s.pool.QueryRow(ctx, `UPDATE accounts SET password_hash = $2, session_epoch = session_epoch + 1, updated_at = now()
		WHERE id = $1 RETURNING session_epoch`, id, newHash).Scan(&epoch)
	return epoch, err
}
