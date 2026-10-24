package tools

import (
	"bytes"
	"context"
	"unicode/utf8"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type fileArgs struct {
	FileID   string `json:"file_id"`
	Offset   int64  `json:"offset"`
	MaxBytes *int   `json:"max_bytes"`
}

type fileWindow struct {
	protocol.ArtifactFile
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	EOF        bool   `json:"eof"`
	Binary     bool   `json:"binary"`
	Text       string `json:"text,omitempty"`
}

// filesRead exposes only finalized result files, with no write or run credentials.
func (s *Service) filesRead(ctx context.Context, c Call) (any, error) {
	var a fileArgs
	if err := decode(c.Arguments, &a); err != nil {
		return nil, err
	}
	count := 16384
	if a.MaxBytes != nil {
		count = *a.MaxBytes
	}
	if a.FileID == "" || a.Offset < 0 || count < 4 || count > 65536 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "file_id, nonnegative offset and max_bytes between 4 and 65536 are required")
	}
	var row store.DeliverableFileRow
	err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		row, err = store.ReadFinalizedDeliverableFile(ctx, tx.Q(), a.FileID, a.Offset, count, s.store.FileStore())
		return notFound(err, "finalized file %s does not exist", a.FileID)
	})
	if err != nil {
		return nil, err
	}
	offset := min(a.Offset, row.Size)
	text, skipped, consumed, binary := fileText(row.Content, offset, row.Size)
	return fileWindow{ArtifactFile: row.ArtifactFile, Offset: offset + int64(skipped),
		NextOffset: offset + int64(consumed), EOF: offset+int64(consumed) >= row.Size,
		Text: text, Binary: binary}, nil
}

// fileText aligns a byte window to complete UTF-8 code points. Binary data is
// identified without serializing raw bytes or loading the rest of the file.
func fileText(data []byte, offset, size int64) (text string, skipped, consumed int, binary bool) {
	start, end := 0, len(data)
	if offset > 0 {
		for start < min(3, len(data)) && !utf8.RuneStart(data[start]) {
			start++
		}
	}
	if offset+int64(end) < size && end > start {
		last := end - 1
		for last > start && !utf8.RuneStart(data[last]) {
			last--
		}
		if !utf8.FullRune(data[last:end]) {
			end = last
		}
	}
	window := data[start:end]
	if !utf8.Valid(window) || bytes.IndexFunc(window, func(r rune) bool {
		return r < 32 && r != '\n' && r != '\r' && r != '\t' || r == 127
	}) >= 0 {
		return "", 0, len(data), true
	}
	return string(window), start, end, false
}
