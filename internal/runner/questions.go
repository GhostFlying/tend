package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ownerQuestions retains question request identities across native tool-call and
// runner restarts. Server answers remain authoritative and are never consumed.
type ownerQuestions struct {
	r        *Runner
	as       protocol.Assignment
	prepared Prepared
	mu       sync.Mutex
}

func (q *ownerQuestions) envelope(payload protocol.AskOwnerPayload) (*protocol.Envelope, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.prepared.StateDir == "" {
		return nil, errors.New("owner question requires persistent run state")
	}
	dir := filepath.Join(q.prepared.StateDir, "owner-questions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare question state: %w", err)
	}
	digest := sha256.Sum256([]byte(payload.QuestionKey))
	path := filepath.Join(dir, hex.EncodeToString(digest[:])+".json")
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	old, err := os.ReadFile(path)
	if err == nil {
		var env protocol.Envelope
		if err := json.Unmarshal(old, &env); err != nil {
			return nil, fmt.Errorf("read question identity: %w", err)
		}
		if !bytes.Equal(env.Payload, body) {
			return nil, errors.New("question_key already has different content; resume with the original question")
		}
		return &env, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	env, err := q.r.newEnvelope(protocol.OpRunsAskOwner, runTarget(q.as), payload)
	if err != nil {
		return nil, err
	}
	record, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	if err := replaceUploadState(path, func(f *os.File) error { _, err := f.Write(record); return err }); err != nil {
		return nil, fmt.Errorf("save question identity: %w", err)
	}
	return env, nil
}

// questionRejection is proof that this exact request was definitively rejected.
// The original intent remains intact; uncertain outcomes never get a receipt.
type questionRejection struct {
	RequestID     string             `json:"request_id"`
	PayloadSHA256 string             `json:"payload_sha256"`
	Operation     protocol.Operation `json:"operation"`
}

func (q *ownerQuestions) rejectionPath(env *protocol.Envelope) string {
	digest := sha256.Sum256([]byte(env.RequestID))
	return filepath.Join(q.prepared.StateDir, "owner-questions", hex.EncodeToString(digest[:])+".rejected")
}

func (q *ownerQuestions) submit(ctx context.Context, env *protocol.Envelope) (protocol.Operation, error) {
	path := q.rejectionPath(env)
	digest := sha256.Sum256(env.Payload)
	payloadHash := hex.EncodeToString(digest[:])
	body, err := os.ReadFile(path)
	if err == nil {
		var receipt questionRejection
		if err := json.Unmarshal(body, &receipt); err != nil {
			return protocol.Operation{}, fmt.Errorf("read question rejection: %w", err)
		}
		if receipt.RequestID != env.RequestID || receipt.PayloadSHA256 != payloadHash || receipt.Operation.Status != protocol.OpFailed || receipt.Operation.Error == nil {
			return protocol.Operation{}, errors.New("question rejection does not match its request")
		}
		return receipt.Operation, receipt.Operation.Error
	}
	if !errors.Is(err, os.ErrNotExist) {
		return protocol.Operation{}, err
	}
	op, err := q.r.send(ctx, runCredential(q.as), env)
	if op.Status == protocol.OpFailed && op.Error != nil {
		body, marshalErr := json.Marshal(questionRejection{RequestID: env.RequestID, PayloadSHA256: payloadHash, Operation: op})
		if marshalErr != nil {
			return protocol.Operation{}, errors.Join(err, marshalErr)
		}
		if writeErr := replaceUploadState(path, func(f *os.File) error { _, err := f.Write(body); return err }); writeErr != nil {
			return protocol.Operation{}, errors.Join(err, fmt.Errorf("save question rejection: %w", writeErr))
		}
	}
	return op, err
}

func (q *ownerQuestions) ask(ctx context.Context, payload protocol.AskOwnerPayload) (protocol.WorkerQuestion, error) {
	if !q.as.WorkerQuestions {
		return protocol.WorkerQuestion{}, errors.New("server has not enabled owner questions for this run")
	}
	env, err := q.envelope(payload)
	if err != nil {
		return protocol.WorkerQuestion{}, err
	}
	op, err := q.submit(ctx, env)
	if err != nil {
		if op.Status == protocol.OpFailed {
			return protocol.WorkerQuestion{}, fmt.Errorf("owner question was rejected; correct the request using a new question_key: %w", err)
		}
		return protocol.WorkerQuestion{}, err
	}
	var question protocol.WorkerQuestion
	if err := json.Unmarshal(op.Result, &question); err != nil {
		return question, fmt.Errorf("decode owner question: %w", err)
	}
	for question.Status == "open" {
		if question.DecisionID == "" {
			return question, errors.New("owner question has no decision identity")
		}
		if err := q.read(ctx, "/"+url.PathEscape(question.DecisionID)+"?wait=25", &question); err != nil {
			return question, err
		}
	}
	if question.Status != "answered" {
		return question, errors.New("owner question is no longer active; the answer does not authorize continuing this run")
	}
	return question, nil
}

func (q *ownerQuestions) recover(ctx context.Context) ([]protocol.WorkerQuestion, error) {
	var questions []protocol.WorkerQuestion
	if err := q.read(ctx, "", &questions); err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(questions))
	for _, question := range questions {
		known[question.QuestionKey] = true
	}
	// A crash can occur after the intent is durable but before its create
	// reaches the server. Finish that exact request before resuming the model.
	entries, err := os.ReadDir(filepath.Join(q.prepared.StateDir, "owner-questions"))
	if errors.Is(err, os.ErrNotExist) {
		return questions, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(q.prepared.StateDir, "owner-questions", entry.Name()))
		if err != nil {
			return nil, err
		}
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, err
		}
		var payload protocol.AskOwnerPayload
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			return nil, err
		}
		if known[payload.QuestionKey] {
			continue
		}
		op, err := q.submit(ctx, &env)
		if op.Status == protocol.OpFailed && op.Error != nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		var question protocol.WorkerQuestion
		if err := json.Unmarshal(op.Result, &question); err != nil {
			return nil, err
		}
		questions = append(questions, question)
		known[question.QuestionKey] = true
	}
	return questions, nil
}

func (q *ownerQuestions) read(ctx context.Context, suffix string, out any) error {
	return q.r.retry(ctx, func() error {
		// One bounded long poll follows the run lifetime. Waiting happens here,
		// never in a model turn or an open database transaction.
		attemptCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, q.r.c.base+"/runner/v1/questions"+suffix, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+q.as.RunToken)
		req.Header.Set(headerRun, q.as.RunID)
		resp, err := q.r.c.http.Do(req)
		if err != nil {
			return &transportError{err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			var failure struct {
				Error *protocol.Error `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
				return &transportError{fmt.Errorf("decode question error: %w", err)}
			}
			if failure.Error == nil {
				return &transportError{fmt.Errorf("question read status %d", resp.StatusCode)}
			}
			if resp.StatusCode >= 500 {
				return &transportError{failure.Error}
			}
			return failure.Error
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return &transportError{fmt.Errorf("decode question read: %w", err)}
		}
		return nil
	})
}
