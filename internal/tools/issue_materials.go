package tools

import (
	"context"
	"io"

	"github.com/GhostFlying/tend/internal/protocol"
)

type hostedIssueMaterialsArgs struct {
	IssueID string `json:"issue_id,omitempty"`
	Kind    string `json:"kind,omitempty"`
	ID      string `json:"id,omitempty"`
	After   string `json:"after,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type issueMaterialWindow struct {
	protocol.IssueMaterialFile
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	EOF        bool   `json:"eof"`
	Binary     bool   `json:"binary"`
	Text       string `json:"text,omitempty"`
}

func (s *Service) issueMaterialWindow(ctx context.Context, issueID, fileID string, offset int64) (issueMaterialWindow, error) {
	file, stream, err := s.app.OpenIssueMaterialFile(ctx, issueID, fileID)
	if err != nil {
		return issueMaterialWindow{}, err
	}
	defer stream.Close()
	offset = min(offset, file.Size)
	if _, err = stream.Seek(offset, io.SeekStart); err != nil {
		return issueMaterialWindow{}, err
	}
	data, err := io.ReadAll(io.LimitReader(stream, 16384))
	if err != nil {
		return issueMaterialWindow{}, err
	}
	text, skipped, consumed, binary := fileText(data, offset, file.Size)
	return issueMaterialWindow{IssueMaterialFile: file, Offset: offset + int64(skipped), NextOffset: offset + int64(consumed), EOF: offset+int64(consumed) >= file.Size, Binary: binary, Text: text}, nil
}
