package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

type mcpFixtureResponse struct {
	JSONRPC string                     `json:"jsonrpc"`
	ID      int                        `json:"id"`
	Result  map[string]json.RawMessage `json:"result"`
	Error   json.RawMessage            `json:"error"`
}

func dockerDuplexFixture(t *testing.T) (*Docker, context.Context, string, Profile) {
	t.Helper()
	image := os.Getenv("VIBER_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("opt-in pinned real Docker engine required")
	}
	broker, err := OpenDocker()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	source := t.TempDir()
	if err = os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "source.txt"), []byte("source-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	lease := testLease()
	lease.InstanceID = fmt.Sprintf("%032x", time.Now().UnixNano())
	lease.ID = lease.InstanceID
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	owned, err := WithOwnership(ctx, Ownership{Lease: lease, Phase: "CHECK", Ordinal: 1}, time.Now().Add(55*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return broker, owned, source, DefaultProfile(image)
}

func readMCPFixtureResponse(reader *bufio.Reader, wantID int) (mcpFixtureResponse, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return mcpFixtureResponse{}, err
	}
	var response mcpFixtureResponse
	if err = json.Unmarshal([]byte(line), &response); err != nil {
		return response, err
	}
	if response.JSONRPC != "2.0" || response.ID != wantID || len(response.Error) != 0 || response.Result == nil {
		return response, fmt.Errorf("invalid MCP response for request %d", wantID)
	}
	return response, nil
}

func TestDockerRunDuplexValidatesInitializeBeforeListAndQuiescesOwnedProcess(t *testing.T) {
	broker, ctx, source, profile := dockerDuplexFixture(t)
	script := `set -eu
test "$(cat /workspace/source.txt)" = source-bytes
if touch /workspace/forbidden 2>/dev/null; then exit 20; fi
IFS= read -r line
case "$line" in *'"method":"initialize"'*) ;; *) exit 30 ;; esac
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}}'
IFS= read -r line
case "$line" in *'"method":"tools/list"'*) ;; *) exit 31 ;; esac
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}'
`
	inv := Invocation{CandidateDigest: c.HashBytes([]byte("duplex-mcp-handshake")), Argv: []string{"/bin/sh", "-c", script}}
	result, err := broker.RunDuplex(ctx, source, profile, inv, func(ctx context.Context, stdout io.Reader, stdin io.Writer) error {
		reader := bufio.NewReader(stdout)
		initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"viber-test","version":"1"}}}` + "\n"
		if _, err := io.WriteString(stdin, initialize); err != nil {
			return err
		}
		response, err := readMCPFixtureResponse(reader, 1)
		if err != nil {
			return err
		}
		var version string
		if err = json.Unmarshal(response.Result["protocolVersion"], &version); err != nil || version != "2025-03-26" {
			return fmt.Errorf("MCP initialize response did not select the requested protocol")
		}
		// The list request is sent only after initialize's JSON-RPC envelope and
		// protocol version have been validated above.
		if _, err = io.WriteString(stdin, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`+"\n"); err != nil {
			return err
		}
		response, err = readMCPFixtureResponse(reader, 2)
		if err != nil {
			return err
		}
		var tools []json.RawMessage
		if err = json.Unmarshal(response.Result["tools"], &tools); err != nil || len(tools) != 0 {
			return fmt.Errorf("unexpected MCP tool list")
		}
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Ownership == nil || result.Ownership.Owner.Phase != "CHECK" || !result.ProcessTreeQuiescent || !result.SourceReadOnly || !result.ProtectedResultChannel {
		t.Fatalf("duplex process was not bound and quiesced: %#v", result)
	}
	if result.DuplexTranscriptDigest == "" || result.DuplexStdinBytes == 0 || result.DuplexStdoutBytes == 0 {
		t.Fatalf("duplex exchange provenance missing: %#v", result)
	}
	if _, err = os.Stat(filepath.Join(source, "forbidden")); !os.IsNotExist(err) {
		t.Fatalf("source mount changed during exchange: %v", err)
	}
	if !strings.Contains(string(result.Stdout), `"serverInfo":{"name":"fixture"`) {
		t.Fatalf("captured server output omitted the handshake: %q", result.Stdout)
	}
}

func TestDockerRunDuplexCancellationAndOversizedPeerOutputQuiesce(t *testing.T) {
	t.Run("cancelled handshake", func(t *testing.T) {
		broker, ctx, source, profile := dockerDuplexFixture(t)
		profile.TimeoutSeconds = 2
		inv := Invocation{CandidateDigest: c.HashBytes([]byte("duplex-cancel")), Argv: []string{"/bin/sh", "-c", `IFS= read -r line; sleep 300`}}
		result, err := broker.RunDuplex(ctx, source, profile, inv, func(ctx context.Context, _ io.Reader, stdin io.Writer) error {
			if _, writeErr := io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"); writeErr != nil {
				return writeErr
			}
			<-ctx.Done()
			return ctx.Err()
		})
		if err == nil || !result.TimedOut || !result.ProcessTreeQuiescent || result.Ownership == nil {
			t.Fatalf("cancelled MCP exchange was not failed closed and quiesced: %#v, %v", result, err)
		}
	})

	t.Run("oversized peer", func(t *testing.T) {
		broker, ctx, source, profile := dockerDuplexFixture(t)
		profile.MaxOutputBytes = 1024
		inv := Invocation{CandidateDigest: c.HashBytes([]byte("duplex-oversized-peer")), Argv: []string{"/bin/sh", "-c", `head -c 8192 /dev/zero`}}
		result, err := broker.RunDuplex(ctx, source, profile, inv, func(_ context.Context, stdout io.Reader, _ io.Writer) error {
			_, readErr := io.Copy(io.Discard, stdout)
			return readErr
		})
		if err == nil || !result.OutputTruncated || len(result.Stdout) != 1024 || !result.ProcessTreeQuiescent || result.Ownership == nil {
			t.Fatalf("oversized peer output was accepted or left live: bytes=%d truncated=%t quiescent=%t err=%v", len(result.Stdout), result.OutputTruncated, result.ProcessTreeQuiescent, err)
		}
	})
}
