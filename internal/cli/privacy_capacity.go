package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/ixayldz/Viber/internal/agent"
	c "github.com/ixayldz/Viber/internal/contracts"
)

func runPrivacyCapacity(args []string, out, errout io.Writer) int {
	f := flags("privacy-capacity", errout)
	directory := f.String("store", "", "existing private store")
	replenish := f.Bool("replenish-control-reserve", false, "replenish bounded physical reserve; does not prune metadata or fences")
	retire := f.Bool("retire-operation-leases", false, "retire only inactive typed restore operation leases")
	jsonMode := f.Bool("json", false, "numeric capacity and fixed reason codes")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if f.NArg() != 0 || *directory == "" || (*replenish && *retire) {
		return report(out, errout, c.Fail(c.InvalidArgument, "privacy capacity requires only an existing store"), *jsonMode)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	id, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	action := "privacy-capacity"
	if *replenish {
		action = "privacy-reserve-replenish"
	}
	if *retire {
		action = "privacy-operation-retire"
	}
	raw, routed, err := ownerCall(ctx, *directory, "", action, id, nil)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var result *agent.PrivacyMetadataStatus
	if routed {
		err = c.DecodeStrict(raw, &result)
	} else {
		var session *agent.Session
		session, err = agent.OpenExisting(ctx, *directory)
		if err == nil {
			defer session.Close()
			if *retire {
				result, err = session.RetirePrivacyOperationLeases(ctx)
			} else if *replenish {
				result, err = session.ReplenishPrivacyControlReserve(ctx)
			} else {
				result, err = session.PrivacyMetadataCapacity(ctx)
			}
		}
	}
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	if result == nil {
		return report(out, errout, c.Fail(c.UnsupportedCapability, "bound privacy authority required"), *jsonMode)
	}
	if *jsonMode {
		if err := jsonWrite(out, result); err != nil {
			return 4
		}
		return 0
	}
	_, err = fmt.Fprintf(out, "Privacy capacity (instant observation; not future admission)\nWork ready: %t\nJournal: %d/%d work records, %d/%d work bytes\nOwner catalog: %d/%d work entries\nControl reserve: %d/%d bytes\nWork blockers: %s\nSupported next actions: %s\n", result.WorkReady, result.Records, result.WorkRecordLimit, result.JournalBytes, result.WorkByteLimit, result.CatalogEntries, result.WorkCatalogLimit, result.PhysicalReserveBytes, result.TargetReserveBytes, strings.Join(result.WorkBlockers, ", "), strings.Join(result.NextActions, ", "))
	if err != nil {
		return 4
	}
	return 0
}
