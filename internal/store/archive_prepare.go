package store

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

func ArchivePreparation(ctx context.Context, q Querier) (protocol.ArchivePreparation, error) {
	var p protocol.ArchivePreparation
	err := q.QueryRowContext(ctx, `SELECT revision,prepared,prepared_at,updated_at FROM archive_preparation WHERE singleton=1`).Scan(&p.Revision, &p.Prepared, &p.PreparedAtMillis, &p.UpdatedAtMillis)
	return p, err
}
