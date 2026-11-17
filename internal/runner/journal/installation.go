package journal

import "context"

func (j *Journal) InstallationVersion(ctx context.Context) (version int64, err error) {
	err = j.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version)
	return version, err
}
