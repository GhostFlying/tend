// This test fixture is built inside the pinned historical source tree. Only
// queue inputs are seeded. The released binaries create all runs and leases.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func seed(path string, count int) error {
	ctx := context.Background()
	db, err := store.Open(ctx, path, time.Now)
	if err != nil {
		return err
	}
	defer db.Close()
	ids := protocol.NewULIDGen(time.Now, nil)
	service := app.New(db, ids, time.Now)
	payload, err := json.Marshal(map[string]any{"title": "historical runner compatibility", "spec": domain.Spec{
		Goal: "return the compatibility report", Acceptance: []string{"result is delivered"},
		Deliverables: []string{domain.DeliveryReport}, Workspace: domain.Workspace{Mode: domain.WorkspaceTemporary},
	}})
	if err != nil {
		return err
	}
	for range count {
		env := &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "tasks.create", Target: protocol.Target{Type: "task", ID: ids.New(protocol.PrefixTask)}, Payload: payload}
		out, err := service.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "fixture", Client: "compatibility-test"}, env, app.CreateTask{})
		if err != nil {
			return err
		}
		if out.Error != nil {
			return out.Error
		}
		fmt.Println(env.Target.ID)
	}
	return nil
}

func main() {
	count := 2
	if len(os.Args) == 3 && os.Args[2] == "rolling" {
		count = 5
	}
	if err := seed(os.Args[1], count); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
