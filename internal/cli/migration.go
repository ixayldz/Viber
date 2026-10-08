package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/store"
)

type migrationStatus struct {
	Snapshot       store.SnapshotInfo      `json:"snapshot"`
	ReadOnlyReason string                  `json:"read_only_reason"`
	Intent         *store.MigrationIntent  `json:"intent,omitempty"`
	Receipt        *store.MigrationReceipt `json:"receipt,omitempty"`
}

func runStoreMigration(command string, args []string, out, errout io.Writer) int {
	f := flags(command, errout)
	directory := f.String("store", "", "existing session store")
	backup := f.String("backup-output", "", "fresh or matching complete pre-migration backup")
	id := f.String("command-id", "", "immutable migration command ID")
	jsonMode := f.Bool("json", false, "JSON receipt/status")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *directory == "" || command == "store-migrate" && (*backup == "" || *id == "") || command == "store-migration-status" && (*backup != "" || *id != "") {
		return report(out, errout, c.Fail(c.InvalidArgument, "store-migrate requires --store, --backup-output and --command-id; store-migration-status requires only --store"), *jsonMode)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	session, err := agent.OpenExisting(ctx, *directory)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var value any
	if command == "store-migrate" {
		value, err = session.Migrate(ctx, *id, *backup)
	} else {
		var info store.SnapshotInfo
		var intent *store.MigrationIntent
		var receipt *store.MigrationReceipt
		info, err = session.Journal.SnapshotInfo(ctx)
		if err == nil {
			intent, receipt, err = session.Journal.MigrationStatus(ctx)
		}
		value = migrationStatus{info, session.Journal.ReadOnlyReason(), intent, receipt}
	}
	err = errors.Join(err, session.Close())
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if *jsonMode {
		if err = jsonWrite(out, value); err != nil {
			return 4
		}
	} else {
		switch result := value.(type) {
		case store.MigrationReceipt:
			fmt.Fprintf(out, "Migration %s committed: schema %d -> %d, journal sequence %d\n", strconv.QuoteToASCII(result.Intent.ID), result.Intent.FromVersion, result.Target.SchemaVersion, result.Target.StoreSeq)
		case migrationStatus:
			fmt.Fprintf(out, "Store schema %d, journal sequence %d\n", result.Snapshot.SchemaVersion, result.Snapshot.StoreSeq)
			if result.ReadOnlyReason != "" {
				fmt.Fprintf(out, "Recovery required: %s\n", strconv.QuoteToASCII(result.ReadOnlyReason))
			}
		}
	}
	return 0
}
