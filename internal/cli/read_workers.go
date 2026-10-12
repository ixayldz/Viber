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

func runReadWorkers(args []string, out, errout io.Writer) int {
	task, args := promptFirst(args)
	f := flags("read-workers", errout)
	directory := f.String("store", "", "private existing task store")
	candidate := f.String("candidate", "", "required exact candidate snapshot SHA-256")
	document := f.String("expected-document", "", "optional current task document SHA-256")
	policyDigest := f.String("expected-policy", "", "optional current effective read-policy SHA-256")
	workerNames := f.String("workers", "inventory,go-graph", "two to four local analyzers: inventory,go-graph,requirements,search")
	changed := f.String("changed-paths", "", "comma-separated paths for Go test selection")
	query := f.String("query", "", "bounded literal query required when search is selected")
	timeout := f.Int64("timeout-ms", 5000, "full parent-budget time ceiling charged before dispatch, 250..30000ms")
	jsonMode := f.Bool("json", false, "structured binding, charge and typed analyzer results")
	if err := f.Parse(args); err != nil {
		return 4
	}
	if task == "" && f.NArg() == 1 {
		task = f.Arg(0)
	} else if f.NArg() != 0 {
		return report(out, errout, c.Fail(c.InvalidArgument, "one task required"), *jsonMode)
	}
	if task == "" || *directory == "" || !c.ValidDigest(*candidate) {
		return report(out, errout, c.Fail(c.InvalidArgument, "read-workers requires a task, store and exact candidate digest"), *jsonMode)
	}
	workers := make([]agent.ReadWorkerKind, 0, 4)
	for _, value := range strings.Split(*workerNames, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			return report(out, errout, c.Fail(c.InvalidArgument, "worker list contains an empty item"), *jsonMode)
		}
		workers = append(workers, agent.ReadWorkerKind(value))
	}
	changedPaths := []string{}
	if strings.TrimSpace(*changed) != "" {
		for _, value := range strings.Split(*changed, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				return report(out, errout, c.Fail(c.InvalidArgument, "changed path list contains an empty item"), *jsonMode)
			}
			changedPaths = append(changedPaths, value)
		}
	}
	operationID, err := resolveCommandID("")
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	request := agent.ReadWorkersRequest{
		OperationID: operationID, CandidateDigest: *candidate, ExpectedDocumentDigest: *document,
		ExpectedPolicyDigest: *policyDigest, Workers: workers, ChangedPaths: changedPaths,
		Query: *query, TimeoutMillis: *timeout,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	raw, routed, err := ownerCall(ctx, *directory, task, "read-workers", operationID, request)
	if err != nil {
		return report(out, errout, err, *jsonMode)
	}
	var result agent.ReadWorkersResult
	if routed {
		if err = c.DecodeStrict(raw, &result); err != nil {
			return report(out, errout, err, *jsonMode)
		}
	} else {
		session, openErr := agent.OpenExisting(ctx, *directory)
		if openErr != nil {
			return report(out, errout, openErr, *jsonMode)
		}
		defer session.Close()
		result, err = session.RunReadWorkers(ctx, task, request)
		if err != nil {
			return report(out, errout, err, *jsonMode)
		}
	}
	if *jsonMode {
		if err = jsonWrite(out, result); err != nil {
			return 4
		}
	} else {
		fmt.Fprintf(out, "Read-only worker fleet %s · task %s · candidate %s · charge %d steps / %d calls / %d ms · result %s\n",
			result.Status, task, result.Binding.CandidateDigest, result.Charge.Admission.ChargedSteps,
			result.Charge.Admission.ChargedToolCalls, result.Charge.Admission.ChargedActiveMillis, result.ResultDigest)
		for _, worker := range result.Workers {
			fmt.Fprintf(out, "Worker %s · result %s\n", worker.Kind, worker.ResultDigest)
		}
		if result.Failure != nil {
			fmt.Fprintf(errout, "%s: %s\n", result.Failure.Code, result.Failure.Message)
		}
	}
	if result.Failure != nil {
		if ctx.Err() != nil {
			return 130
		}
		return 5
	}
	return 0
}
