package serverkit

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"os"
	"slices"

	"github.com/GhostFlying/tend/internal/api"
	"github.com/GhostFlying/tend/internal/archive"
	"github.com/GhostFlying/tend/internal/cfgfile"
	"github.com/GhostFlying/tend/internal/protocol"
)

type archiveOpener func(context.Context, string) (*archive.Reader, error)

// prepareArchives opens each configured archive once. A broken historical source
// is visible to its owner without preventing the current ledger from serving.
func prepareArchives(ctx context.Context, configured map[string]string, log *slog.Logger, open archiveOpener) (map[string]api.ConfiguredArchive, func(), error) {
	entries := map[string]api.ConfiguredArchive{}
	readers := []*archive.Reader{}
	closeAll := func() {
		for _, reader := range readers {
			if err := reader.Close(); err != nil {
				log.Error("close historical archive", "err", err)
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(configured)) {
		if id == "" || configured[id] == "" {
			closeAll()
			return nil, func() {}, errors.New("archives require a nonempty ID and directory")
		}
		directory, err := cfgfile.Path(configured[id])
		if err != nil {
			closeAll()
			return nil, func() {}, err
		}
		reader, err := open(ctx, directory)
		if err != nil {
			entry := api.ConfiguredArchive{State: protocol.ArchiveUnavailable, Reason: "unreadable"}
			if errors.Is(err, archive.ErrUnsupportedSchema) {
				entry.State = protocol.ArchiveUnsupported
				entry.Reason = "unsupported_schema"
			} else if errors.Is(err, os.ErrNotExist) {
				entry.Reason = "missing"
			}
			entries[id] = entry
			log.Warn("historical archive unavailable", "archive_id", id, "err", err)
			continue
		}
		readers = append(readers, reader)
		entries[id] = api.ConfiguredArchive{Reader: reader, State: protocol.ArchiveReady}
	}
	return entries, closeAll, nil
}
