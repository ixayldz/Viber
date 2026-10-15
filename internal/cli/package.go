package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/distribution"
	"github.com/ixayldz/Viber/internal/fileguard"
	"github.com/ixayldz/Viber/internal/store"
)

type packageStores []string

func (p *packageStores) String() string { return fmt.Sprint([]string(*p)) }
func (p *packageStores) Set(value string) error {
	if value == "" || len(*p) >= 64 {
		return c.Fail(c.InvalidArgument, "bounded explicit store paths required")
	}
	*p = append(*p, value)
	return nil
}

func runPackage(args []string, out, errout io.Writer) int {
	if len(args) == 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "package install|status|rollback|deactivate|run required"), false)
	}
	action := args[0]
	switch action {
	case "install", "status", "rollback", "deactivate", "uninstall", "run":
	default:
		return report(out, errout, c.Fail(c.InvalidArgument, "unknown package action"), false)
	}
	f := flags("package "+action, errout)
	prefix := f.String("prefix", "", "managed program directory (defaults to a per-user location), separate from task stores")
	archive := f.String("archive", "", "local portable archive")
	sha := f.String("source-sha", "", "operator-selected full Git commit SHA")
	gh := f.String("gh", "", "absolute operator-selected official GitHub CLI executable")
	target := f.String("revision", "", "exact installed rollback revision")
	launcher := f.Bool("launcher", false, "create an owned per-user `viber` launcher (Windows adds it to PATH; Unix prints a shell PATH command)")
	jsonMode := f.Bool("json", false, "structured program version status")
	var paths packageStores
	f.Var(&paths, "store", "known task store; repeat to hold every used store owner lock during activation")
	if err := f.Parse(args[1:]); err != nil {
		return 4
	}
	if *prefix == "" {
		var prefixErr error
		*prefix, prefixErr = defaultManagedPrefix()
		if prefixErr != nil {
			return report(out, errout, prefixErr, *jsonMode)
		}
	}
	if action != "run" && f.NArg() != 0 || *launcher && action != "install" {
		return report(out, errout, c.Fail(c.InvalidArgument, "run arguments follow --; --launcher is supported only by install"), *jsonMode)
	}
	if action == "run" {
		childStore := ""
		childArgs := f.Args()
		for i := 0; i < len(childArgs); i++ {
			if childArgs[i] == "--" {
				break
			}
			value := ""
			if childArgs[i] == "--store" {
				if i+1 >= len(childArgs) {
					return report(out, errout, c.Fail(c.InvalidArgument, "child store value required"), *jsonMode)
				}
				i++
				value = childArgs[i]
			} else if strings.HasPrefix(childArgs[i], "--store=") {
				value = strings.TrimPrefix(childArgs[i], "--store=")
			}
			if value != "" {
				if childStore != "" {
					return report(out, errout, c.Fail(c.InvalidArgument, "duplicate child store flags denied"), *jsonMode)
				}
				childStore = value
			}
		}
		if childStore != "" {
			absolute, pathErr := filepath.Abs(childStore)
			if pathErr != nil {
				return report(out, errout, pathErr, *jsonMode)
			}
			found := false
			for _, supplied := range paths {
				p, _ := filepath.Abs(supplied)
				found = found || p == absolute
			}
			if !found {
				paths = append(paths, absolute)
			}
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if action != "install" {
		if _, err := os.Lstat(*prefix); err != nil {
			return report(out, errout, c.Fail(c.InvalidArgument, "installed program prefix unavailable"), *jsonMode)
		}
	}
	var bundle distribution.Bundle
	if action == "install" {
		if *archive == "" || !fileguard.Disjoint(*prefix, *archive) {
			return report(out, errout, c.Fail(c.InvalidArgument, "archive must be outside install prefix"), *jsonMode)
		}
		raw, kind, err := distribution.VerifyProvenance(ctx, *archive, *gh, *sha)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
		bundle, err = distribution.ParseArchive(raw, kind, *sha, runtime.GOOS+"/"+runtime.GOARCH)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	if action == "install" || action == "rollback" || action == "deactivate" || action == "uninstall" {
		if err := checkManagedPathOperation(*prefix, action, *launcher); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	// A store owner must really be quiescent: an acknowledgement flag cannot
	// substitute for acquiring its OS writer lock. Compatibility inspection must
	// not change schema, owner generation, or the physical quota protocol.
	m, err := distribution.Open(*prefix)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	defer m.Close()
	registered, err := m.RegisteredStores()
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	for _, known := range registered {
		if err = known.ValidateCurrent(); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		found := false
		for _, supplied := range paths {
			absolute, pathErr := filepath.Abs(supplied)
			if pathErr != nil {
				return report(out, errout, pathErr, *jsonMode)
			}
			if absolute == known.Path {
				found = true
			}
		}
		if !found {
			paths = append(paths, known.Path)
		}
	}
	locked := []*store.Store{}
	defer func() {
		for _, s := range locked {
			_ = s.Close()
		}
	}()
	if action == "install" || action == "rollback" || action == "deactivate" || action == "uninstall" {
		for i, directory := range paths {
			if _, lookupErr := os.Lstat(directory); lookupErr != nil {
				return report(out, errout, c.Fail(c.StaleAuthority, "known store missing; automatic recreation denied"), *jsonMode)
			}
			if !fileguard.Disjoint(*prefix, directory) {
				return report(out, errout, c.Fail(c.PolicyDenied, "program prefix and task store overlap"), *jsonMode)
			}
			for _, previous := range paths[:i] {
				if !fileguard.Disjoint(previous, directory) {
					return report(out, errout, c.Fail(c.InvalidArgument, "duplicate or overlapping store paths"), *jsonMode)
				}
			}
			s, err := store.OpenForCompatibility(ctx, directory)
			if err != nil {
				return report(out, errout, err, *jsonMode)
			}
			locked = append(locked, s)
			info, err := s.SnapshotInfo(ctx)
			if err != nil {
				return report(out, errout, err, *jsonMode)
			}
			if info.SchemaVersion != 2 || info.ReducerVersion != c.ReducerVersion {
				return report(out, errout, c.Fail(c.UnsupportedCapability, "store requires explicit migration before program activation"), *jsonMode)
			}
		}
	}
	for _, directory := range paths {
		if action == "run" {
			if !fileguard.Disjoint(*prefix, directory) {
				return report(out, errout, c.Fail(c.PolicyDenied, "child store overlaps program prefix"), *jsonMode)
			}
			if _, lookupErr := os.Lstat(directory); os.IsNotExist(lookupErr) {
				created, openErr := store.Open(ctx, directory)
				if openErr != nil {
					return report(out, errout, openErr, *jsonMode)
				}
				if closeErr := created.Close(); closeErr != nil {
					return report(out, errout, closeErr, *jsonMode)
				}
			} else if lookupErr != nil {
				return report(out, errout, lookupErr, *jsonMode)
			}
		}
		if err = m.RegisterStore(directory); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	if action == "install" || action == "rollback" {
		// Manager activation reopens every registered owner lock, performs the
		// runtime-capability scan, and holds those locks through selection publish.
		for i := len(locked) - 1; i >= 0; i-- {
			if err = locked[i].Close(); err != nil {
				return report(out, errout, err, *jsonMode)
			}
		}
		locked = nil
	}
	var state distribution.Status
	switch action {
	case "install":
		state, err = m.Install(ctx, bundle)
	case "rollback":
		state, err = m.RollbackContext(ctx, *target)
	case "deactivate":
		state, err = m.Deactivate()
	case "uninstall":
		state, err = m.Uninstall()
	case "status":
		state, err = m.Status()
	case "run":
		code, runErr := m.RunActive(ctx, f.Args(), os.Stdin, out, errout)
		if runErr != nil {
			return report(out, errout, runErr, *jsonMode)
		}
		return code
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	pathInstruction := ""
	switch action {
	case "install", "rollback":
		if err = syncManagedPath(*prefix, state.Binary, *launcher); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		if *launcher && !*jsonMode {
			pathInstruction = managedPathInstruction()
		}
	case "deactivate", "uninstall":
		if err = removeManagedPath(*prefix); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	if *jsonMode {
		if err = jsonWrite(out, state); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Program selection %d · %s · %s\n", state.Active.Sequence, state.Active.Action, state.Active.Revision)
		if state.Binary != "" {
			fmt.Fprintf(out, "Executable: %q\n", state.Binary)
		}
		if pathInstruction != "" {
			fmt.Fprintln(out, pathInstruction)
		}
	}
	return 0
}
