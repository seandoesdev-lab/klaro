package ingestkey

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/platform/audit"
)

// AfterWrite runs inside the same transaction as the mutation it observes.
//
// Key lifecycle events are auditable (design section 3, SOC2), and an audit
// trail that can disagree with the thing it describes is worse than none: if
// the audit write were a second transaction, a crash between the two would
// leave a key with no record of who created it, or a record of a rotation that
// rolled back. Sharing the transaction makes both outcomes impossible.
type AfterWrite func(ctx context.Context, tx pgx.Tx, k Key) error

func runAfter(ctx context.Context, tx pgx.Tx, after AfterWrite, k Key) error {
	if after == nil {
		return nil
	}
	return after(ctx, tx, k)
}

// AuditHook records action against the affected key. actorUserID may be nil for
// machine-initiated changes.
//
// The metadata deliberately carries key_prefix and never the secret or its
// hash: an audit log is read by more people than the key table is.
func AuditHook(rec audit.Recorder, action string, actorUserID *string) AfterWrite {
	if rec == nil {
		return nil
	}
	return func(ctx context.Context, tx pgx.Tx, k Key) error {
		id := k.ID
		meta := map[string]any{
			"name":       k.Name,
			"key_prefix": k.KeyPrefix,
			"status":     k.Status,
		}
		if k.RotatedFromID != nil {
			meta["rotated_from_id"] = *k.RotatedFromID
		}
		if k.GraceUntil != nil {
			meta["grace_until"] = k.GraceUntil
		}
		return rec.RecordTx(ctx, tx, audit.Entry{
			ActorUserID:  actorUserID,
			Action:       action,
			ResourceType: "observability_key",
			ResourceID:   &id,
			Metadata:     meta,
		})
	}
}
