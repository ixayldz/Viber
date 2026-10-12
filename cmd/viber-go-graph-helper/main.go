// viber-go-graph-helper is built into an operator-pinned, minimal Docker image.
// It processes only the exact read-only /workspace mount and one bounded JSON
// request; the host runner provides the hard execution limits.
package main

import (
	"context"
	"io"
	"os"

	"github.com/ixayldz/Viber/internal/intelligence"
)

func main() {
	request, err := io.ReadAll(io.LimitReader(os.Stdin, intelligence.GoSemanticWorkerMaxInput+1))
	if err != nil || len(request) > intelligence.GoSemanticWorkerMaxInput {
		os.Exit(2)
	}
	response, err := intelligence.RunGoSemanticWorker(context.Background(), "/workspace", request)
	if err != nil {
		os.Exit(2)
	}
	if _, err = os.Stdout.Write(response); err != nil {
		os.Exit(2)
	}
}
