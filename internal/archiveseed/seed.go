// Package archiveseed initializes a new offline ledger from explicit static data.
// It is separate from the archive reader: only this fresh-target path opens a store.
package archiveseed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/archive"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type HostBinding struct {
	HostID string   `json:"host_id"`
	Nodes  []string `json:"nodes"`
}
type Bindings struct {
	Hosts []HostBinding `json:"hosts"`
}
type Options struct {
	Target      string
	ArchiveRoot string
	Seed        archive.StaticSeed
	Bindings    Bindings
	Now         func() time.Time
	IDs         protocol.IDGen
	Random      io.Reader
}

type Receipt struct {
	State              string    `json:"state"`
	SourceDeploymentID string    `json:"source_deployment_id"`
	DeploymentID       string    `json:"deployment_id"`
	CreatedAt          time.Time `json:"created_at"`
	Hosts              []string  `json:"hosts"`
}

type Secrets struct {
	ServerSecret string            `json:"server_secret"`
	RunnerTokens map[string]string `json:"runner_tokens"`
}
type BootstrapConfig struct {
	DataDir string `json:"data_dir"`
	app.ConfigPayload
}

// Initialize refuses every existing target, including an empty directory. A
// failure leaves only a non-published new target; it cannot alter an archive or
// prior ledger. It never starts a service or copies network state/journals.
func Initialize(ctx context.Context, options Options) (Receipt, error) {
	var receipt Receipt
	if options.Now == nil || options.IDs == nil || options.Random == nil {
		return receipt, errors.New("seed: clock, IDs and randomness are required")
	}
	if options.Seed.FormatVersion != 1 || options.Seed.SourceDeploymentID == "" || options.ArchiveRoot == "" {
		return receipt, errors.New("seed: explicit static seed and archive root are required")
	}
	target, err := filepath.Abs(options.Target)
	if err != nil {
		return receipt, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return receipt, err
	}
	target = filepath.Join(parent, filepath.Base(target))
	archiveRoot, err := filepath.Abs(options.ArchiveRoot)
	if err != nil {
		return receipt, err
	}
	archiveRoot, err = filepath.EvalSymlinks(archiveRoot)
	if err != nil {
		return receipt, err
	}
	if overlaps(archiveRoot, target) {
		return receipt, errors.New("seed: new target must be separate from the immutable archive")
	}
	bindings := map[string][]string{}
	for _, binding := range options.Bindings.Hosts {
		if binding.HostID == "" || len(binding.Nodes) == 0 || slices.Contains(binding.Nodes, "") {
			return receipt, errors.New("seed: fresh host node bindings are required")
		}
		if _, exists := bindings[binding.HostID]; exists {
			return receipt, errors.New("seed: duplicate host binding")
		}
		bindings[binding.HostID] = slices.Clone(binding.Nodes)
	}
	if len(bindings) != len(options.Seed.Hosts) {
		return receipt, errors.New("seed: explicitly bind exactly the selected hosts")
	}
	secrets := Secrets{RunnerTokens: map[string]string{}}
	randomSecret := func() (string, error) {
		var value [32]byte
		if _, err := io.ReadFull(options.Random, value[:]); err != nil {
			return "", err
		}
		return hex.EncodeToString(value[:]), nil
	}
	secrets.ServerSecret, err = randomSecret()
	if err != nil {
		return receipt, err
	}
	configuration := app.ConfigPayload{}
	for _, repo := range options.Seed.Repositories {
		configuration.Repos = append(configuration.Repos, app.RepoConfig{Repo: repo.Repo, RemoteURL: repo.RemoteURL, BaseCandidates: repo.BaseCandidates, Retired: repo.Retired})
	}
	for _, project := range options.Seed.Projects {
		configuration.Projects = append(configuration.Projects, app.ProjectConfig{Name: project.Name, Title: project.Title, Weight: project.Weight, Repos: project.Repos, Template: project.Template})
	}
	for _, template := range options.Seed.Templates {
		var workspace domain.Workspace
		decoder := json.NewDecoder(bytes.NewReader(template.Workspace))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&workspace); err != nil {
			return receipt, err
		}
		if err = decoder.Decode(new(any)); err != io.EOF {
			return receipt, errors.New("seed: expected one workspace template value")
		}
		if workspace.ModeValue() == domain.WorkspaceReuse || workspace.SourceTaskID != "" || workspace.IncludeExistingChanges {
			return receipt, errors.New("seed: a static template cannot restore old task state")
		}
		configuration.Templates = append(configuration.Templates, app.TemplateConfig{Name: template.Name, Workspace: workspace})
	}
	for _, host := range options.Seed.Hosts {
		nodes, exists := bindings[host.HostID]
		if !exists {
			return receipt, errors.New("seed: selected host lacks a fresh node binding")
		}
		token, err := randomSecret()
		if err != nil {
			return receipt, err
		}
		secrets.RunnerTokens[host.HostID] = token
		digest := sha256.Sum256([]byte(token))
		configuration.Hosts = append(configuration.Hosts, app.HostConfig{HostID: host.HostID, OS: host.OS, Arch: host.Arch, Labels: host.Labels, Repos: host.Repos, Slots: host.Slots, Worker: protocol.WorkerKind(host.Worker), Nodes: nodes, TokenHash: hex.EncodeToString(digest[:])})
	}
	// Ordinary hosted settings are catalog strings, not worker ExecutionProfile
	// overrides. Preserve their values; the normal hosted catalog still decides
	// whether they can be used after activation.
	if options.Seed.MainSettings != nil && options.Seed.MainSettings.Model == "" {
		return receipt, errors.New("seed: selected ordinary settings require a model")
	}
	seenCategories := map[string]bool{}
	for _, category := range options.Seed.Categories {
		if !protocol.AgentInventoryID(category.ID) || strings.TrimSpace(category.Title) == "" || !slices.Contains([]string{"active", "retired"}, category.State) || seenCategories[category.ID] {
			return receipt, errors.New("seed: invalid static category")
		}
		seenCategories[category.ID] = true
	}
	if err = os.Mkdir(target, 0o700); err != nil {
		return receipt, err
	}
	// This is the sole store.Open call: target was just exclusively created above.
	ledger, err := store.Open(ctx, filepath.Join(target, "tend.db"), options.Now)
	if err != nil {
		return receipt, err
	}
	engine := app.New(ledger, options.IDs, options.Now)
	execute := func(principal protocol.Principal, operation string, target protocol.Target, value any, revision *int64, command app.Command) error {
		body, err := json.Marshal(value)
		if err != nil {
			return err
		}
		envelope := &protocol.Envelope{RequestID: options.IDs.New(protocol.PrefixRequest), Operation: operation, Target: target, ExpectedControlRevision: revision, Payload: body}
		result, err := engine.Execute(ctx, principal, envelope, command)
		if err != nil {
			return err
		}
		if result.Error != nil {
			return result.Error
		}
		return nil
	}
	initialized := func() error {
		if err := execute(protocol.Principal{Kind: protocol.PrincipalSystem}, "config.sync", protocol.Target{Type: "config"}, configuration, nil, app.SyncConfig{}); err != nil {
			return err
		}
		for _, category := range options.Seed.Categories {
			zero := int64(0)
			if err := execute(protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "local-initialization", Client: "archive-seed"}, "categories.put", protocol.Target{Type: "category", ID: category.ID}, protocol.PutAgentCategoryPayload{Title: category.Title, Description: category.Description, State: category.State}, &zero, app.PutAgentCategory{}); err != nil {
				return err
			}
		}
		// Ordinary requested settings are static bootstrap data, not a named Agent
		// definition or a claim that a hosted model catalog has been verified.
		if options.Seed.MainSettings != nil {
			if err := ledger.Write(ctx, func(tx *store.Tx) error {
				return tx.PutConversationSettings(ctx, store.ConversationSettings{Model: options.Seed.MainSettings.Model, Effort: options.Seed.MainSettings.Effort, Revision: 1}, options.Now().UnixMilli())
			}); err != nil {
				return err
			}
		}
		identity, err := engine.DeploymentID(ctx)
		if err != nil {
			return err
		}
		if identity == options.Seed.SourceDeploymentID {
			return errors.New("seed: generated identity matches source")
		}
		receipt = Receipt{State: "seeded_offline", SourceDeploymentID: options.Seed.SourceDeploymentID, DeploymentID: identity, CreatedAt: options.Now(), Hosts: []string{}}
		for _, host := range options.Seed.Hosts {
			receipt.Hosts = append(receipt.Hosts, host.HostID)
		}
		return nil
	}()
	if err = errors.Join(initialized, ledger.Close()); err != nil {
		return receipt, err
	}
	// No native config/Agent inventory/network identity is copied. The operator
	// must assemble fresh network/owner/runtime configuration before P8b activation.
	if err = writePrivateJSON(filepath.Join(target, "bootstrap-config.json"), BootstrapConfig{DataDir: target, ConfigPayload: configuration}); err != nil {
		return receipt, err
	}
	if err = writePrivateJSON(filepath.Join(target, "bootstrap-secrets.json"), secrets); err != nil {
		return receipt, err
	}
	if err = writePrivateJSON(filepath.Join(target, "static-seed.json"), options.Seed); err != nil {
		return receipt, err
	}
	if err = writePrivateJSON(filepath.Join(target, "seed-receipt.json"), receipt); err != nil {
		return receipt, err
	}
	directory, err := os.Open(target)
	if err != nil {
		return receipt, err
	}
	err = errors.Join(directory.Sync(), directory.Close())
	if err != nil {
		return receipt, err
	}
	return receipt, nil
}

func overlaps(a, b string) bool {
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}
func writePrivateJSON(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(body, '\n'))
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err = errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("seed: write local output: %w", err)
	}
	return nil
}
