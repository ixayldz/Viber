package extensions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/runner"
)

func TestActualDockerMCPExecuteUsesPinnedReadOnlyNetworkNoneProcess(t *testing.T) {
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in pinned real Docker engine required")
	}
	profile := runner.DefaultProfile(image)
	manifest := Manifest{
		SchemaVersion: SchemaVersion, ID: "actual-fixture", Version: "1.0.0", ProtocolVersion: MCPProtocolVersion,
		ServerName: "fixture-echo", ServerVersion: "1.0.0", Profile: profile,
		Command: []string{"/usr/bin/env", "GOCACHE=/tmp/go-cache", "GOPATH=/tmp/go-path", "GO111MODULE=off", "/usr/local/go/bin/go", "run", "/workspace/server.go"},
		Egress:  EgressPolicy{Mode: EgressNone},
		Tools:   []Tool{{Name: "echo", Description: "Echo a query", InputSchema: []byte(inputSchemaFixture), Effect: EffectReadOnly, Egress: EgressPolicy{Mode: EgressNone}, Idempotency: IdempotencyReadOnly, MaxInputBytes: 1024, MaxOutputBytes: 4096}},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	pin := PinnedManifest{Manifest: manifest}
	canonical, err := c.CanonicalV1(manifest)
	if err != nil {
		t.Fatal(err)
	}
	pin.Digest = c.HashBytes(canonical)
	operationDigest := c.HashBytes([]byte("actual-mcp-operation"))
	admission := Admission{ManifestDigest: pin.Digest, AuthorityDigest: c.HashBytes([]byte("operator-policy")), OperationDigest: operationDigest, GrantedEffects: []Effect{EffectReadOnly}, UserApproved: true}
	broker, err := runner.OpenDocker()
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	source := t.TempDir()
	if err = os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "server.go"), []byte(actualMCPServerSource), 0644); err != nil {
		t.Fatal(err)
	}
	instance := strings.Repeat("1", 32)
	lease := runner.Lease{SchemaVersion: 1, InstanceID: instance, ID: strings.Repeat("2", 32), TaskDigest: c.HashBytes([]byte("actual-task")), OperationDigest: c.HashBytes([]byte("actual-lease-operation")), Generation: 1, PolicyEpoch: 1, SpecVersion: 1, ClockDomain: strings.Repeat("3", 32), DeadlineMillis: 120000}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	owned, err := runner.WithOwnership(ctx, runner.Ownership{Lease: lease, Phase: "CHECK", Ordinal: 1}, time.Now().Add(85*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Execute(owned, broker, source, c.HashBytes([]byte("actual-candidate")), pin, "echo", []byte(`{"query":"hello"}`), admission)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != Completed || result.OperationDigest != operationDigest || !c.ValidDigest(result.RequestDigest) || !c.ValidDigest(result.ResponseDigest) || !c.ValidDigest(result.TranscriptDigest) || !c.ValidDigest(result.SandboxResultDigest) || !result.ProcessTreeQuiescent || result.SandboxOwnership == nil || result.SandboxOwnership.Owner.Lease != lease || len(result.Content) != 1 || result.Content[0].Text != "pong:hello" {
		t.Fatalf("actual MCP response was not fully bound and quiesced: %+v", result)
	}
	if _, err = os.Stat(filepath.Join(source, "forbidden")); !os.IsNotExist(err) {
		t.Fatalf("extension changed its read-only source: %v", err)
	}
}

const actualMCPServerSource = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

const schema = ` + "`" + `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}` + "`" + `

type request struct {
	JSONRPC string ` + "`" + `json:"jsonrpc"` + "`" + `
	ID int64 ` + "`" + `json:"id"` + "`" + `
	Method string ` + "`" + `json:"method"` + "`" + `
	Params struct {
		Name string ` + "`" + `json:"name"` + "`" + `
		Arguments struct { Query string ` + "`" + `json:"query"` + "`" + ` } ` + "`" + `json:"arguments"` + "`" + `
	} ` + "`" + `json:"params"` + "`" + `
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		line, err := reader.ReadBytes(10)
		if err != nil { return }
		var request request
		if json.Unmarshal(line, &request) != nil { return }
		switch request.Method {
		case "notifications/initialized":
			continue
		case "initialize":
			_ = encoder.Encode(map[string]any{"jsonrpc":"2.0","id":request.ID,"result":map[string]any{"protocolVersion":"2025-11-25","capabilities":map[string]any{"tools":map[string]any{"listChanged":false}},"serverInfo":map[string]any{"name":"fixture-echo","version":"1.0.0"}}})
		case "tools/list":
			_ = encoder.Encode(map[string]any{"jsonrpc":"2.0","id":request.ID,"result":map[string]any{"tools":[]any{map[string]any{"name":"echo","description":"Echo a query","inputSchema":json.RawMessage(schema)}}}})
		case "tools/call":
			if request.Params.Name != "echo" { return }
			_ = encoder.Encode(map[string]any{"jsonrpc":"2.0","id":request.ID,"result":map[string]any{"content":[]any{map[string]any{"type":"text","text":fmt.Sprintf("pong:%s", request.Params.Arguments.Query)}}}})
		default:
			return
		}
	}
}`
