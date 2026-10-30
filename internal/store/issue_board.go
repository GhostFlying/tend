package store

import (
	"context"
	"github.com/GhostFlying/tend/internal/protocol"
)

func GetIssueNotificationSettings(ctx context.Context, q Querier) (protocol.IssueNotificationSettings, error) {
	var out protocol.IssueNotificationSettings
	err := q.QueryRowContext(ctx, `SELECT revision,delay_minutes FROM issue_notification_settings WHERE singleton=1`).Scan(&out.Revision, &out.DelayMinutes)
	return out, err
}
