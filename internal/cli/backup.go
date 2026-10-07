package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runStoreBackup(command string, args []string, out, errout io.Writer) int {
	f := flags(command, errout)
	directory := f.String("store", "", "session store path")
	output := f.String("output", "", "new backup directory (existing parent)")
	backup := f.String("backup", "", "backup directory to restore")
	jsonMode := f.Bool("json", false, "JSON manifest")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *directory == "" || command == "store-backup" && (*output == "" || *backup != "") || command == "store-restore" && (*backup == "" || *output != "") {
		return report(out, errout, c.Fail(c.InvalidArgument, "store-backup requires --store and --output; store-restore requires --backup and a new --store"), *jsonMode)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var manifest agent.BackupManifest
	var err error
	if command == "store-backup" {
		// An absent source store must not be created by a backup request.
		if _, err = os.Stat(*directory); err != nil {
			return report(out, errout, err, *jsonMode)
		}
		session, openErr := agent.OpenExisting(ctx, *directory)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		manifest, err = session.Backup(ctx, *output)
		closeErr := session.Close()
		if err == nil {
			err = closeErr
		}
	} else {
		manifest, err = agent.RestoreBackup(ctx, *backup, *directory)
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, manifest); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "%s complete · journal sequence %d · %d immutable files\n", command, manifest.Store.StoreSeq, len(manifest.Files))
	}
	return 0
}
