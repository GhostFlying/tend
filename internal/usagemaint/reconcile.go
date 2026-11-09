// Package usagemaint performs one foreground native-usage reconciliation. It
// never starts an Agent or imports business state from a native provider home.
package usagemaint

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/GhostFlying/tend/internal/harness/nativeusage"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

type Config struct {
	Server, Token, HostID, DeploymentID, NativeHome, Provider, Journal string
	Apply                                                              bool
	// LiveJournal uses the private control transport of the existing runner,
	// whose writer owns the original journal and every pending request.
	LiveJournal bool
	HTTP        *http.Client
	Now         func() time.Time
	IDs         protocol.IDGen
}

type Result struct {
	Applied      bool                   `json:"applied"`
	Sources      int                    `json:"sources"`
	Missing      []protocol.UsageTarget `json:"missing"`
	Preview      protocol.UsagePreview  `json:"preview"`
	Verification protocol.UsagePreview  `json:"verification"`
}

func Run(ctx context.Context, c Config, out io.Writer) error {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.IDs == nil {
		c.IDs = protocol.NewULIDGen(c.Now, rand.Reader)
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: time.Minute}
	}
	if c.Provider != "codex" && c.Provider != "traex" {
		return errors.New("usage reconciliation requires codex or traex")
	}
	call := func(ctx context.Context, method, path string, input, output any) error {
		var body io.Reader
		if input != nil {
			raw, err := json.Marshal(input)
			if err != nil {
				return err
			}
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.Server+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		if c.HostID != "" {
			req.Header.Set("X-Tend-Host", c.HostID)
		}
		if c.DeploymentID != "" {
			req.Header.Set("X-Tend-Deployment-ID", c.DeploymentID)
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("usage reconciliation: transport: %w", err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("usage reconciliation: server rejected request (%d)", res.StatusCode)
		}
		if output == nil {
			return nil
		}
		return json.NewDecoder(res.Body).Decode(output)
	}
	executeOperation := func(ctx context.Context, path string, env protocol.Envelope, out any) error {
		var response struct {
			Operation *protocol.Operation `json:"operation"`
			Error     *protocol.Error     `json:"error"`
		}
		if err := call(ctx, "POST", path, env, &response); err != nil {
			return err
		}
		if response.Error != nil {
			return response.Error
		}
		if response.Operation == nil {
			return errors.New("usage reconciliation: missing operation receipt")
		}
		if response.Operation.Error != nil {
			return response.Operation.Error
		}
		if response.Operation.Status != protocol.OpSucceeded || response.Operation.RequestID != env.RequestID {
			return errors.New("usage reconciliation: receipt is not a matching committed result")
		}
		if out != nil {
			return json.Unmarshal(response.Operation.Result, out)
		}
		return nil
	}
	rpc := func(ctx context.Context, env protocol.Envelope, out any) error {
		return executeOperation(ctx, "/runner/v1/rpc", env, out)
	}
	var targets []protocol.UsageTarget
	if c.HostID != "" {
		env := protocol.Envelope{RequestID: c.IDs.New(protocol.PrefixRequest), Operation: protocol.OpRunnerUsageSources, Target: protocol.Target{Type: "host", ID: c.HostID}, Payload: json.RawMessage(`{}`)}
		if err := rpc(ctx, env, &targets); err != nil {
			return err
		}
	} else if err := call(ctx, "GET", "/api/v1/token-usage/sources", nil, &targets); err != nil {
		return err
	}
	var j *journal.Journal
	deleteRequest := func(ctx context.Context, request string) error {
		if c.LiveJournal {
			// The live writer acknowledges only a committed server receipt.
			return nil
		}
		return j.DeleteRequest(ctx, request)
	}
	registerSource := func(ctx context.Context, source journal.UsageSource) (journal.UsageSourceSnapshot, error) {
		if c.LiveJournal {
			var snapshot journal.UsageSourceSnapshot
			err := call(ctx, "POST", "/runner/v1/usage/source", source, &snapshot)
			return snapshot, err
		}
		if err := j.RegisterUsageSource(ctx, source); err != nil {
			return journal.UsageSourceSnapshot{}, err
		}
		return j.UsageSourceSnapshot(ctx, source.RunID, source.Source.Provider, source.Source.ThreadID)
	}
	queueUsage := func(ctx context.Context, source journal.UsageSource, env *protocol.Envelope) (journal.UsageSourceSnapshot, error) {
		if c.LiveJournal {
			var snapshot journal.UsageSourceSnapshot
			input := journal.UsageQueueRequest{UsageSourceSnapshot: journal.UsageSourceSnapshot{Source: source, ExpectedRevision: source.ExpectedRevision}, Envelope: env}
			err := call(ctx, "POST", "/runner/v1/usage/queue", input, &snapshot)
			return snapshot, err
		}
		if err := j.QueueUsage(ctx, source, env); err != nil {
			return journal.UsageSourceSnapshot{}, err
		}
		return j.UsageSourceSnapshot(ctx, source.RunID, source.Source.Provider, source.Source.ThreadID)
	}
	if c.Apply {
		var err error
		var pending []journal.Request
		if c.LiveJournal {
			if c.HostID == "" {
				return errors.New("live usage reconciliation requires the original runner host")
			}
			err = call(ctx, "GET", "/runner/v1/usage/pending", nil, &pending)
		} else {
			j, err = journal.Open(ctx, c.Journal, c.Now)
			if err != nil {
				return err
			}
			defer j.Close()
			pending, err = j.Requests(ctx)
		}
		if err != nil {
			return err
		}
		for _, p := range pending {
			if p.Envelope.Operation != protocol.OpRunnerUsageUpload && p.Envelope.Operation != "token_usage.reconcile" {
				continue
			}
			if c.HostID != "" {
				err = rpc(ctx, p.Envelope, nil)
			} else {
				err = executeOperation(ctx, "/api/v1/ops", p.Envelope, nil)
			}
			if err != nil {
				return err
			}
			if err = deleteRequest(ctx, p.Envelope.RequestID); err != nil {
				return err
			}
		}
	}
	result := Result{Applied: c.Apply, Missing: []protocol.UsageTarget{}}
	discovery := nativeusage.NewDiscovery().Cycle(c.Now().UnixMilli())
	seen := map[string]map[string]bool{}
	for _, target := range targets {
		if target.Provider != "" && target.Provider != "native" && target.Provider != c.Provider {
			continue
		}
		src, err := nativeusage.Locate(ctx, c.NativeHome, c.Provider, target.ThreadID)
		if err != nil {
			result.Missing = append(result.Missing, target)
			continue
		}
		src.Purpose = target.Purpose
		src.ParentSessionID = target.ParentSessionID
		files, err := discovery.Discover(ctx, src, false)
		if err != nil {
			result.Missing = append(result.Missing, target)
			continue
		}
		result.Sources++
		type batch struct {
			path    string
			cursor  nativeusage.Cursor
			samples []protocol.TokenUsageSample
		}
		var batches []batch
		var all []protocol.TokenUsageSample
		complete := true
		for _, file := range files {
			cursor := nativeusage.Cursor{}
			for {
				page, err := nativeusage.Read(ctx, file, cursor, 256)
				if err != nil {
					return err
				}
				cursor = page.Cursor
				complete = complete && !page.Incomplete
				samples := []protocol.TokenUsageSample{}
				for _, sample := range page.Samples {
					if seen[sample.RecordID] == nil {
						seen[sample.RecordID] = map[string]bool{}
					}
					raw, _ := json.Marshal(sample)
					fingerprint := string(raw)
					if !seen[sample.RecordID][fingerprint] {
						seen[sample.RecordID][fingerprint] = true
						samples = append(samples, sample)
						all = append(all, sample)
					}
				}
				batches = append(batches, batch{path: file.Path, cursor: cursor, samples: samples})
				if !page.More {
					break
				}
			}
		}
		preview := func(samples []protocol.TokenUsageSample) (protocol.UsagePreview, error) {
			input := protocol.UsageUpload{RunID: target.RunID, ConversationID: target.ConversationID, RootThreadID: target.ThreadID, SessionID: src.SessionID, Samples: samples, Coverage: "syncing"}
			var result protocol.UsagePreview
			if c.HostID != "" {
				raw, _ := json.Marshal(input)
				err = rpc(ctx, protocol.Envelope{RequestID: c.IDs.New(protocol.PrefixRequest), Operation: protocol.OpRunnerUsagePreview, Target: protocol.Target{Type: "host", ID: c.HostID}, Payload: raw}, &result)
			} else {
				err = call(ctx, "POST", "/api/v1/token-usage/preview", input, &result)
			}
			return result, err
		}
		groups := previewGroups(all)
		for _, group := range groups {
			p, e := preview(group)
			if e != nil {
				return e
			}
			addPreview(&result.Preview, p)
		}
		if c.Apply {
			hash := sha256.Sum256([]byte(target.RunID + "\x00" + src.Provider + "\x00" + src.Path))
			state := journal.UsageSource{ID: hex.EncodeToString(hash[:]), RunID: target.RunID, ConversationID: target.ConversationID, Source: src, Cursors: map[string]nativeusage.Cursor{}, Files: files, DiscoveredAt: c.Now().UnixMilli(), WriterStopped: target.Stopped}
			snapshot, err := registerSource(ctx, state)
			if err != nil {
				return err
			}
			state = snapshot.Source
			state.ExpectedRevision = snapshot.ExpectedRevision
			if state.Cursors == nil {
				state.Cursors = map[string]nativeusage.Cursor{}
			}
			for i, batch := range batches {
				if previous := state.Cursors[batch.path]; previous.FileID != batch.cursor.FileID || previous.Offset <= batch.cursor.Offset {
					state.Cursors[batch.path] = batch.cursor
				}
				coverage := "syncing"
				if i == len(batches)-1 && complete && state.WriterStopped {
					coverage = "caught_up"
				}
				state.Coverage = coverage
				final := i == len(batches)-1 && complete
				state.Collection.ReadSucceeded(c.Now().UnixMilli(), len(batch.samples) > 0, final, state.WriterStopped, final)
				if len(batch.samples) > 0 {
					state.Collection.Evidence = protocol.UsageObserved
				}
				if i == len(batches)-1 && !complete {
					state.Collection.Failed(c.Now().UnixMilli(), state.WriterStopped, "incomplete_record")
				}
				input := protocol.UsageUpload{RunID: target.RunID, ConversationID: target.ConversationID, RootThreadID: target.ThreadID, SessionID: src.SessionID, Samples: batch.samples, Coverage: coverage}
				operation := protocol.OpRunnerUsageUpload
				value := any(input)
				destination := protocol.Target{Type: "host", ID: c.HostID}
				if c.HostID == "" {
					operation = "token_usage.reconcile"
					value = protocol.UsageReconcile{RunID: target.RunID, ConversationID: target.ConversationID, RootThreadID: target.ThreadID, SessionID: src.SessionID, Coverage: coverage, Samples: batch.samples}
					destination = protocol.Target{Type: "conversation", ID: target.ConversationID}
				}
				raw, _ := json.Marshal(value)
				env := protocol.Envelope{RequestID: c.IDs.New(protocol.PrefixRequest), Operation: operation, Target: destination, Payload: raw}
				snapshot, err = queueUsage(ctx, state, &env)
				if err != nil {
					return err
				}
				state = snapshot.Source
				state.ExpectedRevision = snapshot.ExpectedRevision
				if c.HostID != "" {
					err = rpc(ctx, env, nil)
				} else {
					err = executeOperation(ctx, "/api/v1/ops", env, nil)
				}
				if err != nil {
					return err
				}
				if err = deleteRequest(ctx, env.RequestID); err != nil {
					return err
				}
			}
			for _, group := range groups {
				p, e := preview(group)
				if e != nil {
					return e
				}
				addPreview(&result.Verification, p)
			}
		}
	}

	return json.NewEncoder(out).Encode(result)
}

func addPreview(total *protocol.UsagePreview, p protocol.UsagePreview) {
	total.Records += p.Records
	total.New += p.New
	total.Existing += p.Existing
	total.LinkedLegacy += p.LinkedLegacy
	total.Conflicts += p.Conflicts
	total.TokenDelta += p.TokenDelta
}
