package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func needsSelfUpdate(r store.UpgradeRow) bool { return r.ReleaseSet != nil && r.ReleaseSet.Format >= 2 }

func selfUpdateMatches(r store.UpgradeRow, serviceHash string, o *protocol.UpdaterSelfObservation) bool {
	if !needsSelfUpdate(r) {
		return true
	}
	if o == nil || o.Receipt == nil || o.InstanceID == "" || o.Process.PID <= 1 || o.Process.StartTicks == 0 || o.SupervisedProcess.PID <= 1 {
		return false
	}
	proof := o.Receipt
	if proof.RequestID != r.StartRequestID || proof.UpgradeID != r.View.UpgradeID || proof.ReleaseSetSHA256 != r.ReleaseSetSHA256 || proof.InstanceID != o.InstanceID || proof.CompletedAtMillis <= 0 || proof.RetainedProcess.PID <= 1 || proof.Installed.PID <= 1 || proof.Installed.StartTicks == 0 {
		return false
	}
	os, arch := "", ""
	for _, a := range r.Manifest.Artifacts {
		if a.SHA256 == serviceHash {
			os, arch = a.OS, a.Arch
			break
		}
	}
	for _, a := range r.ReleaseSet.Artifacts {
		if a.Role == "updater" && a.OS == os && a.Arch == arch {
			return a.SHA256 == o.Process.SHA256 && a.SHA256 == proof.Installed.SHA256
		}
	}
	return false
}

func runnerSelfMatches(r store.UpgradeRow, h store.UpgradeHost, rt protocol.RunnerRuntime) bool {
	if !needsSelfUpdate(r) {
		return true
	}
	return rt.Updater != nil && rt.Updater.InstanceID == h.HostID && selfUpdateMatches(r, h.TargetHash, rt.Updater) && rt.Updater.SupervisedProcess.SHA256 == rt.Build.BinarySHA256 && int64(rt.Updater.SupervisedProcess.PID) == rt.Build.PID && rt.Updater.SupervisedProcess.LaunchID == rt.InstanceID
}
