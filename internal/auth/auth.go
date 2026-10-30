// Package auth manages admin accounts, password hashing and role permissions.
package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/fuad71/ecommerce/internal/db"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrEmailTaken         = errors.New("an admin with this email already exists")
	ErrWeakPassword       = errors.New("password must be at least 10 characters")
)

const (
	RoleAdmin = "admin"
	RoleStaff = "staff"
)

// Permission names checked by the admin router.
type Permission string

const (
	PermViewDashboard Permission = "dashboard.view"
	PermManageOrders  Permission = "orders.manage"
	PermViewCustomers Permission = "customers.view"
	PermManageCatalog Permission = "catalog.manage"
	PermManageStock   Permission = "inventory.manage"
	PermManageSetting Permission = "settings.manage"
	PermViewAudit     Permission = "audit.view"
	PermManageOffers  Permission = "offers.manage"
	// PermManageCourier sends orders to Steadfast; staff fulfil orders, so they have it.
	PermManageCourier Permission = "courier.manage"
)

var rolePerms = map[string]map[Permission]bool{
	RoleAdmin: {
		PermViewDashboard: true, PermManageOrders: true, PermViewCustomers: true,
		PermManageCatalog: true, PermManageStock: true, PermManageSetting: true, PermViewAudit: true,
		PermManageOffers: true, PermManageCourier: true,
	},
	RoleStaff: {
		PermViewDashboard: true, PermManageOrders: true, PermViewCustomers: true, PermManageCourier: true,
	},
}

type Admin struct {
	ID          int64
	Email       string
	Name        string
	Role        string
	Active      bool
	LastLoginAt *time.Time
	CreatedAt   time.Time
}

func (a *Admin) Can(p Permission) bool { return a != nil && a.Active && rolePerms[a.Role][p] }

type Service struct{ pool *pgxpool.Pool }

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// dummyHash keeps login timing similar for unknown emails.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("timing-equaliser-password"), bcrypt.DefaultCost)

func (s *Service) Authenticate(ctx context.Context, email, password string) (*Admin, error) {
	var a Admin
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT id, email, name, role, active, password_hash, created_at
		FROM admins WHERE lower(email) = lower($1)`, strings.TrimSpace(email)).
		Scan(&a.ID, &a.Email, &a.Name, &a.Role, &a.Active, &hash, &a.CreatedAt)
	if db.IsNoRows(err) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || !a.Active {
		return nil, ErrInvalidCredentials
	}
	_, _ = s.pool.Exec(ctx, `UPDATE admins SET last_login_at = now() WHERE id = $1`, a.ID)
	return &a, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Admin, error) {
	var a Admin
	err := s.pool.QueryRow(ctx, `SELECT id, email, name, role, active, last_login_at, created_at
		FROM admins WHERE id = $1`, id).
		Scan(&a.ID, &a.Email, &a.Name, &a.Role, &a.Active, &a.LastLoginAt, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Service) List(ctx context.Context) ([]Admin, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, email, name, role, active, last_login_at, created_at
		FROM admins ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Admin, error) {
		var a Admin
		err := r.Scan(&a.ID, &a.Email, &a.Name, &a.Role, &a.Active, &a.LastLoginAt, &a.CreatedAt)
		return a, err
	})
}

func (s *Service) Create(ctx context.Context, email, name, password, role string) (*Admin, error) {
	if len(password) < 10 {
		return nil, ErrWeakPassword
	}
	if role != RoleAdmin && role != RoleStaff {
		return nil, errors.New("role must be admin or staff")
	}
	email = strings.TrimSpace(email)
	if !strings.Contains(email, "@") || strings.TrimSpace(name) == "" {
		return nil, errors.New("valid email and name are required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, err
	}
	var a Admin
	err = s.pool.QueryRow(ctx, `INSERT INTO admins (email, name, password_hash, role) VALUES ($1, $2, $3, $4)
		RETURNING id, email, name, role, active, created_at`, email, strings.TrimSpace(name), hash, role).
		Scan(&a.ID, &a.Email, &a.Name, &a.Role, &a.Active, &a.CreatedAt)
	if db.IsUniqueViolation(err, "admins_email_uq") {
		return nil, ErrEmailTaken
	}
	return &a, err
}

func (s *Service) Count(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM admins`).Scan(&n)
	return n, err
}

var ErrLastAdmin = errors.New("at least one active admin must remain")

// Update changes an account's name, role and active flag. It refuses to demote or
// deactivate the last active admin, so the store can never be locked out.
func (s *Service) Update(ctx context.Context, id int64, name, role string, active bool) error {
	if role != RoleAdmin && role != RoleStaff {
		return errors.New("role must be admin or staff")
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialise concurrent admin changes.
		if _, err := tx.Exec(ctx, `LOCK TABLE admins IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		if role != RoleAdmin || !active {
			var others int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM admins WHERE role = 'admin' AND active AND id <> $1`, id).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return ErrLastAdmin
			}
		}
		_, err := tx.Exec(ctx, `UPDATE admins SET name = $2, role = $3, active = $4, updated_at = now() WHERE id = $1`,
			id, strings.TrimSpace(name), role, active)
		// Deactivated accounts lose access immediately: loadAdmin rejects inactive admins.
		return err
	})
}

func (s *Service) SetPassword(ctx context.Context, id int64, password string) error {
	if len(password) < 10 {
		return ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE admins SET password_hash = $2, updated_at = now() WHERE id = $1`, id, hash)
	return err
}

// ChangeOwnPassword verifies the current password before setting a new one.
func (s *Service) ChangeOwnPassword(ctx context.Context, id int64, current, next string) error {
	var hash string
	if err := s.pool.QueryRow(ctx, `SELECT password_hash FROM admins WHERE id = $1`, id).Scan(&hash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
		return ErrInvalidCredentials
	}
	return s.SetPassword(ctx, id, next)
}
