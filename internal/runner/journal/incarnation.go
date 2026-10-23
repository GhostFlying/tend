package journal

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
)

// NextProcessGeneration allocates one strictly increasing process incarnation.
// This small, separate database MUST NOT be restored with the business journal:
// a rollback starts another process, rather than returning to an older identity.
// It contains no task state, credentials or schema-dependent business records.
func (j *Journal) NextProcessGeneration(ctx context.Context) (int64, error) {
	// Creation keeps local process metadata private without rewriting existing
	// files or replacing damaged state with a fresh counter.
	f, err := os.OpenFile(j.path+".incarnation", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return 0, fmt.Errorf("journal: create process generations: %w", err)
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	db, err := sql.Open("sqlite", "file:"+j.path+".incarnation?"+q.Encode())
	if err != nil {
		return 0, fmt.Errorf("journal: open process generations: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS process_generation (id INTEGER PRIMARY KEY CHECK(id=1), generation INTEGER NOT NULL CHECK(generation>0)) STRICT`); err != nil {
		return 0, fmt.Errorf("journal: initialize process generations: %w", err)
	}
	var generation int64
	err = db.QueryRowContext(ctx, `INSERT INTO process_generation(id,generation) VALUES(1,1) ON CONFLICT(id) DO UPDATE SET generation=generation+1 RETURNING generation`).Scan(&generation)
	if err != nil {
		return 0, fmt.Errorf("journal: allocate process generation: %w", err)
	}
	return generation, nil
}
