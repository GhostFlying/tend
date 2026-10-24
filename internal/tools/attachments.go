package tools

import (
	"context"
	"io"

	"github.com/GhostFlying/tend/internal/protocol"
)

type attachmentWindow struct {
	protocol.ConversationAttachment
	LocalPath       string `json:"local_path,omitempty"`
	DownloadCommand string `json:"download_command,omitempty"`
	Offset          int64  `json:"offset"`
	NextOffset      int64  `json:"next_offset"`
	EOF             bool   `json:"eof"`
	Binary          bool   `json:"binary"`
	Text            string `json:"text,omitempty"`
}

func (s *Service) attachmentsRead(ctx context.Context, c Call) (any, error) {
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
	meta, stream, err := s.app.OpenConversationAttachment(ctx, a.FileID)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	offset := min(a.Offset, meta.Size)
	if _, err := stream.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(stream, int64(count)))
	if err != nil {
		return nil, err
	}
	text, skipped, consumed, binary := fileText(data, offset, meta.Size)
	result := attachmentWindow{ConversationAttachment: meta, Offset: offset + int64(skipped), NextOffset: offset + int64(consumed), EOF: offset+int64(consumed) >= meta.Size, Binary: binary, Text: text}
	if c.Principal.Kind == protocol.PrincipalMainAgent && !c.Principal.Addon() {
		result.LocalPath, err = s.store.FileStore().Path(meta.SHA256)
		if err != nil {
			return nil, err
		}
	} else {
		result.DownloadCommand = "tend attachment download " + meta.FileID + " --out <local-path>"
	}
	return result, nil
}
