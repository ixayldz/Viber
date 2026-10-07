package agent

import (
	"context"
	"os"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

// OpenExisting is for control commands. A typo in --store must not initialize a
// new database or private archive as a side effect of asking for task status.
func OpenExisting(ctx context.Context, directory string) (*Session, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	info, err := root.Lstat("state.sqlite")
	if err != nil {
		root.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		root.Close()
		return nil, c.Fail(c.PolicyDenied, "session database must be a regular file")
	}
	f, err := root.Open("state.sqlite")
	if err != nil {
		root.Close()
		return nil, err
	}
	opened, err := f.Stat()
	if err == nil && !os.SameFile(info, opened) {
		err = c.Fail(c.Conflict, "session database replaced during open")
	}
	if err == nil {
		err = fileguard.SingleLink(f)
	}
	f.Close()
	root.Close()
	if err != nil {
		return nil, err
	}
	return Open(ctx, directory)
}
