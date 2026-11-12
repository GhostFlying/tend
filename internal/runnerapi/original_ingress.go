package runnerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"

	"github.com/GhostFlying/tend/internal/protocol"
)

// readEnvelope classifies unknown lengths within the inline decoder budget.
// Only actual large bodies need admission before reading their remainder and
// the separate processing slot. Standard decoding and App staging materialize
// one real original; retain the slot through dispatch, not just Decode. The
// temporary transport wrapper is never a second retained native original.
func (s *Server) readEnvelope(r *http.Request, admitLarge func() error) (protocol.Envelope, func(), error) {
	var env protocol.Envelope
	noop := func() {}
	// Closing the HTTP body interrupts a stalled read on caller cancellation.
	stop := context.AfterFunc(r.Context(), func() { _ = r.Body.Close() })
	defer stop()
	large := r.ContentLength > maxBody
	var prefix []byte
	if r.ContentLength < 0 {
		var err error
		prefix, err = io.ReadAll(io.LimitReader(requestOriginalReader{r.Context(), r.Body}, maxBody+1))
		if err != nil {
			return env, noop, envelopeDecodeError(r.Context(), err)
		}
		large = len(prefix) > maxBody
		if !large {
			err := json.NewDecoder(bytes.NewReader(prefix)).Decode(&env)
			return env, noop, envelopeDecodeError(r.Context(), err)
		}
	}
	if !large {
		err := json.NewDecoder(io.LimitReader(r.Body, maxBody+1)).Decode(&env)
		return env, noop, envelopeDecodeError(r.Context(), err)
	}
	if err := admitLarge(); err != nil {
		return env, noop, err
	}
	file, err := os.CreateTemp("", "tend-runner-original-*")
	if err != nil {
		return env, noop, err
	}
	cleanup := func() { _ = file.Close(); _ = os.Remove(file.Name()) }
	if _, err = file.Write(prefix); err != nil {
		cleanup()
		return env, noop, err
	}
	_, err = io.CopyBuffer(file, requestOriginalReader{r.Context(), r.Body}, make([]byte, 64<<10))
	if err != nil {
		cleanup()
		return env, noop, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return env, noop, err
	}
	select {
	case s.originals <- struct{}{}:
	case <-r.Context().Done():
		cleanup()
		return env, noop, r.Context().Err()
	}
	release := func() { cleanup(); <-s.originals }
	err = json.NewDecoder(requestOriginalReader{r.Context(), file}).Decode(&env)
	return env, release, envelopeDecodeError(r.Context(), err)
}

type requestOriginalReader struct {
	ctx  context.Context
	body io.Reader
}

func (r requestOriginalReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.body.Read(p)
}

// IO/cancellation failures are not malformed originals. Preserve them for the
// ordinary transport error path instead of making a permanent payload rejection.
func envelopeDecodeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var syntax *json.SyntaxError
	var wrongType *json.UnmarshalTypeError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &syntax) || errors.As(err, &wrongType) {
		return protocol.Errorf(protocol.CodeInvalidRequest, "body is not an envelope")
	}
	return err
}
