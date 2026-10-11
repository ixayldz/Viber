package extensions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/runner"
)

const inputSchemaFixture = `{"type":"object","properties":{"query":{"type":"string","maxLength":100}},"required":["query"],"additionalProperties":false}`
const outputSchemaFixture = `{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`

func fixturePin(t *testing.T) PinnedManifest {
	t.Helper()
	manifest := Manifest{
		SchemaVersion: SchemaVersion, ID: "fixture.echo", Version: "1.0.0", ProtocolVersion: MCPProtocolVersion,
		ServerName: "fixture-echo", ServerVersion: "1.0.0", Profile: runner.DefaultProfile("fixture@sha256:" + strings.Repeat("a", 64)),
		Command: []string{"/usr/local/bin/fixture-mcp"}, Egress: EgressPolicy{Mode: EgressNone},
		Tools: []Tool{{Name: "echo", Description: "Return the query", InputSchema: json.RawMessage(inputSchemaFixture), OutputSchema: json.RawMessage(outputSchemaFixture), Effect: EffectReadOnly, Egress: EgressPolicy{Mode: EgressNone}, Idempotency: IdempotencyReadOnly, MaxInputBytes: 1024, MaxOutputBytes: 1024}},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("fixture manifest invalid: %v", err)
	}
	canonical, err := c.CanonicalV1(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return PinnedManifest{Manifest: manifest, Digest: c.HashBytes(canonical)}
}

func fixtureAdmission(pin PinnedManifest) Admission {
	return Admission{ManifestDigest: pin.Digest, AuthorityDigest: c.HashBytes([]byte("authority")), OperationDigest: c.HashBytes([]byte("operation")), GrantedEffects: []Effect{EffectReadOnly}, UserApproved: true}
}

func TestExecuteNegotiatesPinnedMCPAndReturnsUntrustedProvenance(t *testing.T) {
	pin := fixturePin(t)
	broker := &fixtureDuplexBroker{tool: pin.Manifest.Tools[0]}
	result, err := Execute(context.Background(), broker, "C:/candidate", c.HashBytes([]byte("candidate")), pin, "echo", json.RawMessage(`{"query":"hello"}`), fixtureAdmission(pin))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if broker.callCount != 1 || result.Outcome != Completed || !result.EffectPossible || result.ToolError || len(result.Content) != 1 || result.Content[0].Text != "hello" {
		t.Fatalf("unexpected call result: calls=%d result=%+v", broker.callCount, result)
	}
	if result.ManifestDigest != pin.Digest || result.AuthorityDigest == "" || result.ToolSchemaDigest == "" || result.ArgumentsDigest == "" || result.RequestDigest == "" || result.ResponseDigest == "" || result.TranscriptDigest == "" || result.SandboxResultDigest == "" || !result.ProcessTreeQuiescent {
		t.Fatalf("incomplete provenance: %+v", result)
	}
}

func TestExecuteRejectsToolSchemaDriftBeforeCall(t *testing.T) {
	pin := fixturePin(t)
	broker := &fixtureDuplexBroker{tool: pin.Manifest.Tools[0], schemaDrift: true}
	result, err := Execute(context.Background(), broker, "C:/candidate", c.HashBytes([]byte("candidate")), pin, "echo", json.RawMessage(`{"query":"hello"}`), fixtureAdmission(pin))
	if err == nil || broker.callCount != 0 || result.Outcome != NotDispatched || result.EffectPossible {
		t.Fatalf("schema drift dispatched or was accepted: calls=%d result=%+v err=%v", broker.callCount, result, err)
	}
}

func TestExecuteRecordsUnknownAfterAmbiguousToolDispatch(t *testing.T) {
	pin := fixturePin(t)
	broker := &fixtureDuplexBroker{tool: pin.Manifest.Tools[0], dropToolResponse: true}
	result, err := Execute(context.Background(), broker, "C:/candidate", c.HashBytes([]byte("candidate")), pin, "echo", json.RawMessage(`{"query":"hello"}`), fixtureAdmission(pin))
	if err == nil || broker.callCount != 1 || result.Outcome != Unknown || !result.EffectPossible || result.RequestDigest == "" {
		t.Fatalf("ambiguous dispatch was not retained as UNKNOWN: calls=%d result=%+v err=%v", broker.callCount, result, err)
	}
}

func TestValidateJSONRejectsUnsupportedSchemaAndUnknownInput(t *testing.T) {
	schema := []byte(inputSchemaFixture)
	if err := ValidateJSON(schema, []byte(`{"query":"hello"}`)); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if err := ValidateJSON(schema, []byte(`{"query":"hello","extra":true}`)); err == nil {
		t.Fatal("unknown input property accepted")
	}
	if err := ValidateJSON([]byte(`{"type":"string","pattern":".*"}`), []byte(`"value"`)); err == nil {
		t.Fatal("unsupported schema keyword accepted")
	}
}

type fixtureDuplexBroker struct {
	tool             Tool
	schemaDrift      bool
	dropToolResponse bool
	callCount        int
}

func (b *fixtureDuplexBroker) RunDuplex(ctx context.Context, source string, profile runner.Profile, invocation runner.Invocation, driver runner.DuplexDriver) (runner.Result, error) {
	if invocation.Stdin != nil || invocation.CandidateDigest == "" || len(invocation.Argv) == 0 {
		return runner.Result{}, errors.New("unexpected invocation contract")
	}
	serverOutputReader, serverOutputWriter := io.Pipe()
	serverInputReader, serverInputWriter := io.Pipe()
	var output bytes.Buffer
	var outputMu sync.Mutex
	serverOutput := &lockedWriter{writer: io.MultiWriter(serverOutputWriter, &lockedBuffer{buffer: &output, mutex: &outputMu})}
	serverInput := &countingReader{reader: serverInputReader}
	serverDone := make(chan error, 1)
	go func() {
		defer serverOutputWriter.Close()
		defer serverInputReader.Close()
		serverDone <- b.serve(serverInput, serverOutput)
	}()

	driverErr := driver(ctx, serverOutputReader, serverInputWriter)
	_ = serverInputWriter.Close()
	serverErr := <-serverDone
	_ = serverInputReader.Close()
	_ = serverOutputReader.Close()
	outputMu.Lock()
	stdout := bytes.Clone(output.Bytes())
	outputMu.Unlock()
	if driverErr != nil {
		serverErr = nil // The protocol error is the authoritative failure.
	}
	boundInvocation := invocation
	empty := []byte{}
	boundInvocation.Stdin = &empty
	invocationDigest, _ := c.Digest(boundInvocation)
	runResult := runner.Result{
		SchemaVersion: c.SchemaVersion, CandidateDigest: invocation.CandidateDigest,
		ProfileDigest: c.HashBytes([]byte("profile")), InvocationDigest: invocationDigest,
		ExitCode: 0, Stdout: stdout, SourceReadOnly: true, ProtectedResultChannel: true,
		ProcessTreeQuiescent: true, DuplexTranscriptDigest: c.HashBytes(append(bytes.Clone(stdout), []byte("transcript")...)),
		DuplexStdinBytes: serverInput.count, DuplexStdoutBytes: int64(len(stdout)),
	}
	if serverErr != nil {
		return runResult, serverErr
	}
	return runResult, driverErr
}

func (b *fixtureDuplexBroker) serve(input io.Reader, output io.Writer) error {
	reader := bufio.NewReader(input)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var envelope struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := c.DecodeStrict(bytes.TrimSuffix(line, []byte{'\n'}), &envelope); err != nil || envelope.JSONRPC != "2.0" {
			return errors.New("invalid client RPC request")
		}
		if envelope.Method == "notifications/initialized" {
			continue
		}
		var id int64
		if c.DecodeStrict(envelope.ID, &id) != nil {
			return errors.New("invalid client request id")
		}
		switch envelope.Method {
		case "initialize":
			if err := writeFixtureResponse(output, id, map[string]any{
				"protocolVersion": MCPProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]any{"name": "fixture-echo", "version": "1.0.0"},
			}); err != nil {
				return err
			}
		case "tools/list":
			inputSchema := json.RawMessage(b.tool.InputSchema)
			if b.schemaDrift {
				inputSchema = json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)
			}
			tool := map[string]any{"name": b.tool.Name, "description": b.tool.Description, "inputSchema": inputSchema}
			if b.tool.Title != "" {
				tool["title"] = b.tool.Title
			}
			if len(b.tool.OutputSchema) > 0 {
				tool["outputSchema"] = json.RawMessage(b.tool.OutputSchema)
			}
			if err := writeFixtureResponse(output, id, map[string]any{"tools": []any{tool}}); err != nil {
				return err
			}
		case "tools/call":
			b.callCount++
			if b.dropToolResponse {
				return nil
			}
			var params struct {
				Name      string `json:"name"`
				Arguments struct {
					Query string `json:"query"`
				} `json:"arguments"`
			}
			if c.DecodeStrict(envelope.Params, &params) != nil || params.Name != b.tool.Name {
				return errors.New("invalid tool call request")
			}
			if err := writeFixtureResponse(output, id, map[string]any{
				"content":           []any{map[string]any{"type": "text", "text": params.Arguments.Query}},
				"structuredContent": map[string]any{"ok": true},
				"isError":           false,
			}); err != nil {
				return err
			}
		default:
			return errors.New("unexpected client method")
		}
	}
}

func writeFixtureResponse(writer io.Writer, id int64, result any) error {
	body, err := c.CanonicalV1(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		return err
	}
	return writeAll(writer, append(body, '\n'))
}

type lockedWriter struct {
	writer io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) { return w.writer.Write(p) }

type lockedBuffer struct {
	buffer *bytes.Buffer
	mutex  *sync.Mutex
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.buffer.Write(p)
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.count += int64(n)
	return n, err
}
