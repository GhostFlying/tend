package gitws

import (
	"errors"
	"time"

	"github.com/GhostFlying/tend/internal/harness/orca"
)

func (w *Workspaces) owner() *Workspaces {
	if w.shared != nil {
		return w.shared
	}
	return w
}

// AddBackend creates a layout view sharing this host's sole bindings and locks.
// Assembly calls it before the runner begins; it never creates another Home.
func (w *Workspaces) AddBackend(id string, client *orca.Client) error {
	if id != "git" && id != "orca" {
		return errors.New("unsupported workspace backend")
	}
	root := w.owner()
	if root.backends == nil {
		root.backends = map[string]*Workspaces{}
	}
	view := &Workspaces{cfg: root.cfg, git: root.git, shared: root, backendID: id}
	if id == "orca" {
		if client == nil {
			return errors.New("orca backend needs its existing client")
		}
		view.layout = orcaLayout{w: view, orca: client, poll: 5 * time.Second}
	} else {
		view.layout = clones{view}
	}
	root.backends[id] = view
	return nil
}
func (w *Workspaces) Backend(id string) (*Workspaces, error) {
	view := w.owner().backends[id]
	if view == nil {
		return nil, errors.New("original workspace backend is not configured")
	}
	return view, nil
}
