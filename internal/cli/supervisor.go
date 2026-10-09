package cli

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/ipc"
	"github.com/ixayldz/Viber/internal/owner"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Test helpers can run the actual CLI in a child test process. Production always
// launches this same executable with fixed argv, without a shell or PATH lookup.
var supervisorExecutable = os.Executable
var supervisorPrefix = []string{}

func probeSupervisor(ctx context.Context, directory string) (ipc.Info, bool, error) {
	info, err := ipc.Discover(directory)
	if errors.Is(err, ipc.ErrNoOwner) {
		return info, false, nil
	}
	if err != nil {
		return info, false, err
	}
	id, err := ipc.NewID()
	if err != nil {
		return info, false, err
	}
	response, err := ipc.Call(ctx, info, ipc.Request{ID: id, Command: "owner-status", Payload: json.RawMessage("null")})
	if err != nil {
		var call *ipc.CallError
		if errors.As(err, &call) && !call.Dispatched && ctx.Err() == nil {
			return info, false, nil
		}
		return info, false, err
	}
	var status owner.SupervisorStatus
	if c.DecodeStrict(response.Result, &status) != nil || status.SchemaVersion != 1 || status.Owner != info || status.Closing {
		return info, false, c.Fail(c.StoreOwned, "owner is closing or its readiness binding changed")
	}
	return info, true, nil
}
func startBackgroundOwner(ctx context.Context, directory string) (ipc.Info, error) {
	info, ready, err := probeSupervisor(ctx, directory)
	if err != nil || ready {
		return info, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return info, err
	}
	defer root.Close()
	if err = fileguard.Private(root); err != nil {
		return info, err
	}
	if err = fileguard.RegularPath(root, "supervisor-start.lock", true); err != nil {
		return info, err
	}
	lock, err := fileguard.Lock(root, "supervisor-start.lock")
	if err != nil {
		return info, err
	}
	defer lock.Close()
	info, ready, err = probeSupervisor(ctx, directory)
	if err != nil || ready {
		return info, err
	}
	// A dead descriptor is reconciled only by acquiring the real owner lock.
	// A responsive but unreachable live writer never authorizes a second process.
	session, err := agent.OpenExisting(ctx, directory)
	if err != nil {
		return info, err
	}
	if err = session.Close(); err != nil {
		return info, err
	}
	executable, err := supervisorExecutable()
	if err != nil {
		return info, err
	}
	if !filepath.IsAbs(executable) {
		return info, c.Fail(c.PolicyDenied, "background executable must be absolute")
	}
	id, err := ipc.NewID()
	if err != nil {
		return info, err
	}
	stdout, err := root.OpenFile("owner-launch-"+id+".stdout", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return info, err
	}
	defer stdout.Close()
	stderr, err := root.OpenFile("owner-launch-"+id+".stderr", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return info, err
	}
	defer stderr.Close()
	argv := append(append([]string{}, supervisorPrefix...), "serve", "--store", root.Name(), "--json")
	command := exec.Command(executable, argv...)
	command.Stdin = nil
	command.Stdout = stdout
	command.Stderr = stderr
	detachProcess(command)
	if err = command.Start(); err != nil {
		return info, err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return info, c.Fail(c.UnknownOutcome, "background owner launch acknowledgment interrupted; inspect owner-status before relaunching")
		case <-deadline.C:
			return info, c.Fail(c.UnknownOutcome, "background owner launch acknowledgment timed out; inspect owner-status before relaunching")
		case <-done:
			return info, c.Fail(c.StoreOwned, "background owner exited before readiness; inspect private launch log")
		case <-ticker.C:
			info, ready, err = probeSupervisor(ctx, directory)
			if err != nil {
				return info, err
			}
			if ready {
				if info.PID != command.Process.Pid {
					return info, c.Fail(c.Conflict, "a different process published the owner endpoint")
				}
				return info, nil
			}
		}
	}
}
func runSupervisor(action string, args []string, out, errout io.Writer) int {
	f := flags(action, errout)
	directory := f.String("store", "", "existing private store")
	jsonMode := f.Bool("json", false, "structured result")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if *directory == "" || f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "existing store required"), *jsonMode)
	}
	ctx := context.Background()
	var value any
	var err error
	if action == "serve-background" {
		value, err = startBackgroundOwner(ctx, *directory)
	} else {
		id, _ := ipc.NewID()
		raw, routed, callErr := ownerCall(ctx, *directory, "", action, id, nil)
		err = callErr
		if err == nil && !routed {
			err = c.Fail(c.StoreOwned, "no responsive owner; use serve-background or inspect the store")
		}
		if err == nil && action == "owner-stop" {
			var status owner.SupervisorStatus
			if c.DecodeStrict(raw, &status) != nil {
				err = c.Fail(c.StoreIntegrityError, "invalid owner shutdown response")
			} else {
				err = waitSupervisorStopped(ctx, *directory, status.Owner.ID)
			}
		}
		value = json.RawMessage(raw)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if err = jsonWrite(out, value); err != nil {
		return 4
	}
	return 0
}

func waitSupervisorStopped(ctx context.Context, directory, ownerID string) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, discoverErr := ipc.Discover(directory)
		if discoverErr == nil && info.ID != ownerID {
			return c.Fail(c.Conflict, "another owner appeared during shutdown; do not stop it with the old request")
		}
		if discoverErr != nil && !errors.Is(discoverErr, ipc.ErrNoOwner) {
			return discoverErr
		}
		if errors.Is(discoverErr, ipc.ErrNoOwner) {
			if err = fileguard.RegularPath(root, "owner.lock", false); err != nil {
				return err
			}
			lock, lockErr := fileguard.Lock(root, "owner.lock")
			if lockErr == nil {
				return lock.Close()
			}
			var typed *c.Error
			if !errors.As(lockErr, &typed) || typed.Code != c.StoreOwned {
				return lockErr
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return c.Fail(c.UnknownOutcome, "owner shutdown acknowledgment is incomplete; inspect its current endpoint and store lock")
		case <-ticker.C:
		}
	}
}
