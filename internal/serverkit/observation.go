package serverkit

import (
	"context"
	"os"
	"runtime/debug"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/updater"
)

// deploymentObservation binds local updater proof to this actual executable,
// not the newest batch (which can update only runners or fail before install).
func deploymentObservation(cfg *UpgradeConfig, ids protocol.IDGen) (app.Option, error) {
	hash, err := executableSHA256()
	if err != nil {
		return nil, err
	}
	actual := protocol.ObservedServer{BinarySHA256: hash, PID: int64(os.Getpid()), ProcessInstanceID: os.Getenv("TEND_UPDATER_PROCESS_INSTANCE_ID")}
	if actual.ProcessInstanceID == "" {
		actual.ProcessInstanceID = ids.New(protocol.PrefixLaunch)
	}
	if build, ok := debug.ReadBuildInfo(); ok {
		actual.Version = build.Main.Version
		for _, s := range build.Settings {
			if s.Key == "vcs.revision" {
				actual.Commit = s.Value
			}
			if s.Key == "vcs.modified" {
				actual.Modified = s.Value == "true"
			}
		}
	}
	socket := ""
	if cfg != nil {
		socket = cfg.UpdaterSocket
	}
	return app.WithDeploymentObserver(func(ctx context.Context) (protocol.ObservedDeployment, error) {
		instance := ""
		if cfg != nil {
			instance = cfg.Instance
		}
		d, err := configuredDeploymentObservation(ctx, actual, socket, instance)
		if cfg != nil && cfg.ExecutionEntry != nil {
			entry := *cfg.ExecutionEntry
			d.Preparation.ExecutionEntry = &entry
		}
		return d, err
	}), nil
}

func configuredDeploymentObservation(ctx context.Context, actual protocol.ObservedServer, socket, instance string) (protocol.ObservedDeployment, error) {
	d, err := readDeploymentObservation(ctx, actual, socket)
	d.Preparation = &protocol.ObservedDeploymentPreparation{ServerInstance: instance}
	if d.CommittedProof != nil && instance != "" && d.CommittedProof.Binding.InstanceID != instance {
		d.ProofStatus, d.CommittedProof = "mismatch", nil
	}
	return d, err
}

func readDeploymentObservation(ctx context.Context, actual protocol.ObservedServer, socket string) (protocol.ObservedDeployment, error) {
	result := protocol.ObservedDeployment{ActualServer: &actual, ProofStatus: "unavailable"}
	if socket == "" {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	state, err := updater.CallSocket(ctx, socket, "status", updater.ControlRequest{})
	if err != nil {
		if ctx.Err() != nil && ctx.Err() != context.DeadlineExceeded {
			return result, ctx.Err()
		}
		return result, nil
	}
	return observedDeploymentProof(actual, state), nil
}

func observedDeploymentProof(actual protocol.ObservedServer, state updater.State) protocol.ObservedDeployment {
	result := protocol.ObservedDeployment{ActualServer: &actual, ProofStatus: "unconfirmed"}
	receipt := updater.CommittedReceipt(state)
	if receipt == nil {
		return result
	}
	if state.Process.PID != int(actual.PID) || receipt.CurrentProcessInstanceID != actual.ProcessInstanceID || receipt.CurrentSHA256 != actual.BinarySHA256 {
		result.ProofStatus = "mismatch"
		return result
	}
	result.ProofStatus = "confirmed"
	result.CommittedProof = receipt
	return result
}
