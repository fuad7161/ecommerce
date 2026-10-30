// Package audit records important administrative actions.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/fuad71/ecommerce/internal/db"
)

type Entry struct {
	ID        int64
	AdminName string
	Action    string
	Entity    string
	EntityID  string
	Details   string
	IP        string
	CreatedAt time.Time
}

// Log writes an audit entry. Failures are logged but never block the action itself.
func Log(ctx context.Context, q db.Querier, adminID int64, ip, action, entity, entityID string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	b, _ := json.Marshal(details)
	var admin *int64
	if adminID > 0 {
		admin = &adminID
	}
	_, err := q.Exec(ctx, `INSERT INTO audit_logs (admin_id, action, entity, entity_id, details, ip)
		VALUES ($1, $2, $3, $4, $5, $6)`, admin, action, entity, entityID, b, ip)
	if err != nil {
		slog.ErrorContext(ctx, "audit log write failed", "action", action, "err", err)
	}
}

func Recent(ctx context.Context, q db.Querier, limit, offset int) ([]Entry, error) {
	rows, err := q.Query(ctx, `SELECT l.id, coalesce(a.name, 'system'), l.action, l.entity, l.entity_id,
			l.details::text, l.ip, l.created_at
		FROM audit_logs l LEFT JOIN admins a ON a.id = l.admin_id
		ORDER BY l.created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Entry, error) {
		var e Entry
		err := r.Scan(&e.ID, &e.AdminName, &e.Action, &e.Entity, &e.EntityID, &e.Details, &e.IP, &e.CreatedAt)
		return e, err
	})
}
