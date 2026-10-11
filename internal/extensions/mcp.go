package extensions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/runner"
)

// DuplexBroker is the narrow runner surface used by MCP. Implementations must
// own and quiesce the pinned sandbox process; extension code is never started
// directly by this package.
type DuplexBroker interface {
	RunDuplex(context.Context, string, runner.Profile, runner.Invocation, runner.DuplexDriver) (runner.Result, error)
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

var errRPCErrorResponse = errors.New("extension returned a JSON-RPC error response")

type exchange struct {
	reader         *bufio.Reader
	writer         io.Writer
	serverBytes    bytes.Buffer
	outbound       int
	inbound        int
	outboundSize   int
	nextID         int64
	callSent       bool
	requestDigest  string
	responseDigest string
	serverName     string
	serverVersion  string
	content        []TextContent
	structured     json.RawMessage
	toolError      bool
	errorCode      int64
	errorMessage   string
}

// Execute performs one read-only or sandbox-compute MCP call in the pinned,
// network-none runner. The server must complete initialize and exact tools/list
// conformance before this function sends tools/call. The call is sent at most
// once; any ambiguous failure after that point is recorded as UNKNOWN.
func Execute(ctx context.Context, broker DuplexBroker, sourceDir, candidateDigest string, pin PinnedManifest, toolName string, arguments json.RawMessage, admission Admission) (ToolResult, error) {
	result := newToolResult(pin, toolName, admission)
	if ctx == nil || broker == nil || !c.ValidDigest(candidateDigest) {
		return result, c.Fail(c.InvalidArgument, "extension execution requires a sandbox broker and bound candidate")
	}
	if err := pin.Validate(); err != nil {
		return result, err
	}
	tool, ok := pin.Manifest.Tool(toolName)
	if !ok {
		return result, c.Fail(c.PolicyDenied, "extension tool is absent from the pinned manifest")
	}
	if err := admission.Validate(pin, tool); err != nil {
		return result, err
	}
	if len(arguments) == 0 || len(arguments) > tool.MaxInputBytes {
		return result, c.Fail(c.InvalidArgument, "extension arguments exceed the pinned input bound")
	}
	if err := ValidateJSON(tool.InputSchema, arguments); err != nil {
		return result, err
	}
	canonicalArguments, err := c.CanonicalV1(json.RawMessage(arguments))
	if err != nil || len(canonicalArguments) > tool.MaxInputBytes {
		return result, c.Fail(c.InvalidArgument, "extension arguments cannot be canonically represented within their pinned bound")
	}
	result.ArgumentsDigest = c.HashBytes(arguments)
	result.ToolSchemaDigest, err = tool.SchemaDigest()
	if err != nil {
		return result, c.Fail(c.InvalidArgument, "pinned extension schema digest unavailable")
	}
	result.OperationDigest = admission.OperationDigest

	x := &exchange{nextID: 1}
	driver := func(driverCtx context.Context, stdout io.Reader, stdin io.Writer) error {
		x.reader = bufio.NewReaderSize(stdout, 4096)
		x.writer = stdin
		if err := x.initialize(driverCtx, pin.Manifest); err != nil {
			return err
		}
		if err := x.listTools(driverCtx, pin.Manifest); err != nil {
			return err
		}
		if err := x.callTool(driverCtx, tool, canonicalArguments); err != nil {
			return err
		}
		return nil
	}

	invocation := runner.Invocation{CandidateDigest: candidateDigest, Argv: append([]string(nil), pin.Manifest.Command...), RequestDigest: admission.OperationDigest}
	runResult, runErr := broker.RunDuplex(ctx, sourceDir, pin.Manifest.Profile, invocation, driver)
	result.SandboxResultDigest, _ = c.Digest(runResult)
	result.SandboxOwnership = runResult.Ownership
	result.TranscriptDigest = runResult.DuplexTranscriptDigest
	result.ProcessTreeQuiescent = runResult.ProcessTreeQuiescent
	result.RequestDigest = x.requestDigest
	expectedInvocationDigest := invocationDigest(invocation)
	if expectedInvocationDigest == "" || runResult.InvocationDigest != expectedInvocationDigest {
		runErr = errors.Join(runErr, c.Fail(c.StoreIntegrityError, "extension invocation receipt differs from the admitted request"))
	}
	if runResult.OutputTruncated || runResult.DuplexTranscriptDigest == "" || runResult.DuplexStdinBytes < 1 || runResult.DuplexStdoutBytes < 1 {
		runErr = errors.Join(runErr, c.Fail(c.StoreIntegrityError, "extension duplex transcript is missing or exceeded its bound"))
	}
	// Compare the complete captured stdout with bytes the protocol driver parsed.
	// This rejects extra messages that arrived after the final response.
	if !bytes.Equal(runResult.Stdout, x.serverBytes.Bytes()) {
		runErr = errors.Join(runErr, c.Fail(c.PolicyDenied, "extension server emitted unconsumed or malformed protocol output"))
	}
	if runErr != nil || !runResult.ProcessTreeQuiescent {
		result.Outcome = NotDispatched
		if x.callSent {
			result.Outcome = Unknown
			result.EffectPossible = true
		}
		var dispatchFailure *runner.DispatchFailure
		if errors.As(runErr, &dispatchFailure) && dispatchFailure.EffectPossible && x.callSent {
			result.Outcome = Unknown
			result.EffectPossible = true
		}
		if runErr == nil {
			runErr = c.Fail(c.PolicyDenied, "extension process did not reach quiescence")
		}
		return result, runErr
	}
	if x.responseDigest == "" || x.requestDigest == "" {
		result.Outcome = NotDispatched
		return result, c.Fail(c.PolicyDenied, "extension call has no validated tool response")
	}
	result.Outcome = Completed
	result.EffectPossible = true
	result.ToolError = x.toolError
	result.ErrorCode = x.errorCode
	result.ErrorMessage = x.errorMessage
	result.Content = append([]TextContent(nil), x.content...)
	result.StructuredContent = bytes.Clone(x.structured)
	result.ResponseDigest = x.responseDigest
	result.ServerName = x.serverName
	result.ServerVersion = x.serverVersion
	if x.toolError {
		result.Outcome = CompletedError
	}
	return result, nil
}

func invocationDigest(invocation runner.Invocation) string {
	empty := []byte{}
	invocation.Stdin = &empty // RunDuplex binds its interactive OpenStdin mode.
	digest, _ := c.Digest(invocation)
	return digest
}

func newToolResult(pin PinnedManifest, toolName string, admission Admission) ToolResult {
	tool, _ := pin.Manifest.Tool(toolName)
	return ToolResult{
		SchemaVersion: SchemaVersion, ExtensionID: pin.Manifest.ID, ExtensionVersion: pin.Manifest.Version,
		ManifestDigest: pin.Digest, ProtocolVersion: pin.Manifest.ProtocolVersion, ToolName: toolName,
		AuthorityDigest: admission.AuthorityDigest, Effect: tool.Effect,
		Egress:      EgressPolicy{Mode: tool.Egress.Mode, Origins: append([]string(nil), tool.Egress.Origins...)},
		Idempotency: tool.Idempotency, Outcome: NotDispatched,
	}
}

func (x *exchange) initialize(ctx context.Context, manifest Manifest) error {
	params := struct {
		ProtocolVersion string   `json:"protocolVersion"`
		Capabilities    struct{} `json:"capabilities"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}{ProtocolVersion: MCPProtocolVersion}
	params.ClientInfo.Name = "viber-extension-broker"
	params.ClientInfo.Version = "1"
	response, _, err := x.request(ctx, "initialize", params)
	if err != nil {
		return err
	}
	var payload map[string]json.RawMessage
	if err := decodeObject(response.Result, &payload); err != nil || !onlyKeys(payload, "protocolVersion", "capabilities", "serverInfo", "instructions", "_meta") {
		return c.Fail(c.PolicyDenied, "extension initialize response is malformed or advertises unsupported fields")
	}
	protocol, err := rawString(payload["protocolVersion"], 64)
	if err != nil || protocol != manifest.ProtocolVersion {
		return c.Fail(c.PolicyDenied, "extension protocol revision does not match its pin")
	}
	var capabilities map[string]json.RawMessage
	if err := decodeObject(payload["capabilities"], &capabilities); err != nil || !onlyKeys(capabilities, "tools", "_meta") {
		return c.Fail(c.UnsupportedCapability, "extension advertised unpinned MCP capabilities")
	}
	var toolsCapability map[string]json.RawMessage
	if err := decodeObject(capabilities["tools"], &toolsCapability); err != nil || !onlyKeys(toolsCapability, "listChanged", "_meta") {
		return c.Fail(c.UnsupportedCapability, "extension tools capability is malformed")
	}
	if rawBool(toolsCapability["listChanged"], false) != nil {
		return c.Fail(c.UnsupportedCapability, "dynamic extension tool changes are not supported")
	}
	var serverInfo map[string]json.RawMessage
	if err := decodeObject(payload["serverInfo"], &serverInfo); err != nil || !onlyKeys(serverInfo, "name", "version", "title", "websiteUrl", "icons", "_meta") {
		return c.Fail(c.PolicyDenied, "extension server identity is malformed")
	}
	name, nameErr := rawString(serverInfo["name"], 128)
	version, versionErr := rawString(serverInfo["version"], 128)
	if nameErr != nil || versionErr != nil || name != manifest.ServerName || version != manifest.ServerVersion {
		return c.Fail(c.StaleAuthority, "extension server identity does not match its pin")
	}
	x.serverName = name
	x.serverVersion = version
	if err := x.notification(ctx, "notifications/initialized"); err != nil {
		return err
	}
	return nil
}

func (x *exchange) listTools(ctx context.Context, manifest Manifest) error {
	seen := make(map[string]Tool, len(manifest.Tools))
	for _, item := range manifest.Tools {
		seen[item.Name] = item
	}
	cursor := ""
	seenCursor := map[string]bool{}
	for page := 0; page < MaxRPCListPages; page++ {
		params := map[string]string{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		response, _, err := x.request(ctx, "tools/list", params)
		if err != nil {
			return err
		}
		var payload map[string]json.RawMessage
		if err := decodeObject(response.Result, &payload); err != nil || !onlyKeys(payload, "tools", "nextCursor", "_meta") {
			return c.Fail(c.PolicyDenied, "extension tools/list response is malformed")
		}
		var listed []json.RawMessage
		if err := c.DecodeStrict(payload["tools"], &listed); err != nil || len(listed) > MaxManifestTools {
			return c.Fail(c.UnsupportedCapability, "extension tools/list exceeds its bound")
		}
		for _, rawTool := range listed {
			if err := compareServerTool(rawTool, seen); err != nil {
				return err
			}
		}
		if len(payload["nextCursor"]) == 0 {
			if len(seen) != 0 {
				return c.Fail(c.StaleAuthority, "extension tools/list omitted a pinned tool")
			}
			return nil
		}
		cursor, err = rawString(payload["nextCursor"], 512)
		if err != nil || cursor == "" || seenCursor[cursor] {
			return c.Fail(c.PolicyDenied, "extension tools/list cursor is invalid or repeats")
		}
		seenCursor[cursor] = true
		if len(seen)+len(listed) > MaxManifestTools {
			return c.Fail(c.UnsupportedCapability, "extension tool pagination exceeds its bound")
		}
	}
	return c.Fail(c.UnsupportedCapability, "extension tools/list exceeded its page bound")
}

func compareServerTool(raw json.RawMessage, remaining map[string]Tool) error {
	var fields map[string]json.RawMessage
	if err := decodeObject(raw, &fields); err != nil || !onlyKeys(fields, "name", "title", "description", "inputSchema", "outputSchema", "annotations", "_meta") {
		return c.Fail(c.PolicyDenied, "extension advertised a malformed or unsupported tool")
	}
	name, err := rawString(fields["name"], 128)
	if err != nil {
		return c.Fail(c.PolicyDenied, "extension tool name is invalid")
	}
	pinned, ok := remaining[name]
	if !ok {
		return c.Fail(c.StaleAuthority, "extension advertised an unpinned tool")
	}
	description, err := rawString(fields["description"], 2048)
	if err != nil || description != pinned.Description {
		return c.Fail(c.StaleAuthority, "extension tool description differs from its pin")
	}
	if pinned.Title == "" {
		if len(fields["title"]) != 0 {
			return c.Fail(c.StaleAuthority, "extension tool title differs from its pin")
		}
	} else {
		title, err := rawString(fields["title"], 128)
		if err != nil || title != pinned.Title {
			return c.Fail(c.StaleAuthority, "extension tool title differs from its pin")
		}
	}
	if !sameCanonicalJSON(fields["inputSchema"], pinned.InputSchema) || !sameOptionalCanonicalJSON(fields["outputSchema"], pinned.OutputSchema) {
		return c.Fail(c.StaleAuthority, "extension tool schema differs from its pin")
	}
	delete(remaining, name)
	return nil
}

func (x *exchange) request(ctx context.Context, method string, params any) (rpcEnvelope, []byte, error) {
	var empty rpcEnvelope
	if err := ctx.Err(); err != nil {
		return empty, nil, err
	}
	x.nextID++
	request := rpcRequest{JSONRPC: "2.0", ID: x.nextID, Method: method, Params: params}
	body, err := c.CanonicalV1(request)
	if err != nil || len(body)+1 > MaxRPCMessageBytes || x.outbound >= MaxRPCMessages || x.outboundSize+len(body)+1 > 64<<10 {
		return empty, nil, c.Fail(c.UnsupportedCapability, "extension outbound RPC message exceeds its bound")
	}
	if method == "tools/call" {
		// Set this before writing: even a partial newline-delimited request can
		// be interpreted by a server after a transport failure.
		x.callSent = true
		x.requestDigest = c.HashBytes(body)
	}
	if err := writeAll(x.writer, append(body, '\n')); err != nil {
		return empty, nil, err
	}
	x.outbound++
	x.outboundSize += len(body) + 1
	response, raw, err := x.readResponse(ctx, request.ID)
	if err != nil {
		return empty, nil, err
	}
	x.responseDigest = c.HashBytes(raw)
	if len(response.Error) != 0 {
		return response, body, errRPCErrorResponse
	}
	return response, body, nil
}

func (x *exchange) notification(ctx context.Context, method string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := c.CanonicalV1(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
	}{JSONRPC: "2.0", Method: method})
	if err != nil || len(body)+1 > MaxRPCMessageBytes || x.outbound >= MaxRPCMessages || x.outboundSize+len(body)+1 > 64<<10 {
		return c.Fail(c.UnsupportedCapability, "extension notification exceeds its bound")
	}
	if err := writeAll(x.writer, append(body, '\n')); err != nil {
		return err
	}
	x.outbound++
	x.outboundSize += len(body) + 1
	return nil
}

func (x *exchange) readResponse(ctx context.Context, expectedID int64) (rpcEnvelope, []byte, error) {
	var envelope rpcEnvelope
	if x.inbound >= MaxRPCMessages {
		return envelope, nil, c.Fail(c.UnsupportedCapability, "extension inbound RPC count exceeds its bound")
	}
	line, err := readBoundedLine(ctx, x.reader, MaxRPCMessageBytes)
	if err != nil {
		return envelope, nil, err
	}
	x.inbound++
	x.serverBytes.Write(line)
	raw := bytes.TrimSuffix(line, []byte{'\n'})
	if !utf8.Valid(raw) || c.DecodeStrict(raw, &envelope) != nil || envelope.JSONRPC != "2.0" || len(envelope.ID) == 0 || envelope.Method != "" || len(envelope.Params) != 0 || len(envelope.Result) == 0 && len(envelope.Error) == 0 {
		return envelope, raw, c.Fail(c.PolicyDenied, "extension sent a malformed or unsolicited JSON-RPC message")
	}
	var actualID int64
	if err := c.DecodeStrict(envelope.ID, &actualID); err != nil || actualID != expectedID || (len(envelope.Result) > 0) == (len(envelope.Error) > 0) {
		return envelope, raw, c.Fail(c.PolicyDenied, "extension JSON-RPC response id or shape is invalid")
	}
	return envelope, raw, nil
}

func readBoundedLine(ctx context.Context, reader *bufio.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0, min(limit, 4096))
	for len(line) <= limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > limit {
			return nil, c.Fail(c.UnsupportedCapability, "extension RPC line exceeds its bound")
		}
		line = append(line, part...)
		if err == nil {
			if len(line) < 2 || line[len(line)-2] == '\r' {
				return nil, c.Fail(c.PolicyDenied, "extension RPC requires newline-delimited UTF-8 JSON")
			}
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return nil, c.Fail(c.UnsupportedCapability, "extension RPC line exceeds its bound")
}

func writeAll(writer io.Writer, body []byte) error {
	for len(body) > 0 {
		n, err := writer.Write(body)
		if n > 0 {
			body = body[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func decodeObject(raw []byte, target *map[string]json.RawMessage) error {
	if len(raw) == 0 || len(raw) > MaxRPCMessageBytes {
		return c.Fail(c.InvalidArgument, "extension RPC object missing or oversized")
	}
	if err := c.DecodeStrict(raw, target); err != nil || *target == nil {
		return c.Fail(c.InvalidArgument, "extension RPC value must be an object")
	}
	return nil
}

func onlyKeys(object map[string]json.RawMessage, keys ...string) bool {
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func rawString(raw json.RawMessage, maxBytes int) (string, error) {
	var value string
	if len(raw) == 0 || c.DecodeStrict(raw, &value) != nil || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return "", c.Fail(c.InvalidArgument, "invalid bounded extension protocol string")
	}
	return value, nil
}

func rawBool(raw json.RawMessage, expected bool) error {
	if len(raw) == 0 {
		if expected {
			return c.Fail(c.InvalidArgument, "required extension protocol boolean missing")
		}
		return nil
	}
	var value bool
	if c.DecodeStrict(raw, &value) != nil || value != expected {
		return c.Fail(c.UnsupportedCapability, "extension protocol boolean does not match the pinned capability")
	}
	return nil
}

func sameCanonicalJSON(left, right []byte) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	l, errLeft := c.CanonicalV1(json.RawMessage(left))
	r, errRight := c.CanonicalV1(json.RawMessage(right))
	return errLeft == nil && errRight == nil && bytes.Equal(l, r)
}

func sameOptionalCanonicalJSON(left, right []byte) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == 0 && len(right) == 0
	}
	return sameCanonicalJSON(left, right)
}

// parseToolResult validates the untrusted response against the manifest. It
// deliberately accepts only text content and bounded structured JSON.
func parseToolResult(raw json.RawMessage, tool Tool) ([]TextContent, json.RawMessage, bool, error) {
	var fields map[string]json.RawMessage
	if err := decodeObject(raw, &fields); err != nil || !onlyKeys(fields, "content", "structuredContent", "isError", "_meta") {
		return nil, nil, false, c.Fail(c.PolicyDenied, "extension tool result is malformed")
	}
	var contentRaw []json.RawMessage
	if err := c.DecodeStrict(fields["content"], &contentRaw); err != nil || len(contentRaw) > 64 {
		return nil, nil, false, c.Fail(c.UnsupportedCapability, "extension result content exceeds its bound")
	}
	content := make([]TextContent, 0, len(contentRaw))
	totalBytes := 0
	for _, rawItem := range contentRaw {
		var item map[string]json.RawMessage
		if decodeObject(rawItem, &item) != nil || !onlyKeys(item, "type", "text", "annotations", "_meta") {
			return nil, nil, false, c.Fail(c.PolicyDenied, "extension returned unsupported content")
		}
		kind, kindErr := rawString(item["type"], 32)
		text, textErr := rawStringAllowEmpty(item["text"], MaxToolOutputBytes)
		if kindErr != nil || kind != "text" || textErr != nil {
			return nil, nil, false, c.Fail(c.UnsupportedCapability, "extension returned non-text or invalid content")
		}
		totalBytes += len(text)
		if totalBytes > tool.MaxOutputBytes {
			return nil, nil, false, c.Fail(c.UnsupportedCapability, "extension result content exceeds its pinned output bound")
		}
		content = append(content, TextContent{Text: text})
	}
	var structured json.RawMessage
	if len(fields["structuredContent"]) > 0 {
		if len(tool.OutputSchema) == 0 || len(fields["structuredContent"]) > tool.MaxOutputBytes {
			return nil, nil, false, c.Fail(c.UnsupportedCapability, "extension structured output is not pinned or exceeds its bound")
		}
		if err := ValidateJSON(tool.OutputSchema, fields["structuredContent"]); err != nil {
			return nil, nil, false, c.Fail(c.PolicyDenied, "extension structured output fails its pinned schema")
		}
		structured = bytes.Clone(fields["structuredContent"])
	}
	toolError := false
	if len(fields["isError"]) > 0 {
		var err error
		toolError, err = rawBoolValue(fields["isError"])
		if err != nil {
			return nil, nil, false, c.Fail(c.PolicyDenied, "extension isError flag is malformed")
		}
	}
	return content, structured, toolError, nil
}

func rawStringAllowEmpty(raw json.RawMessage, maxBytes int) (string, error) {
	var value string
	if len(raw) == 0 || c.DecodeStrict(raw, &value) != nil || len(value) > maxBytes || !utf8.ValidString(value) {
		return "", c.Fail(c.InvalidArgument, "invalid bounded extension text")
	}
	return value, nil
}

func rawBoolValue(raw json.RawMessage) (bool, error) {
	var value bool
	if len(raw) == 0 || c.DecodeStrict(raw, &value) != nil {
		return false, c.Fail(c.InvalidArgument, "invalid extension boolean")
	}
	return value, nil
}

func (x *exchange) consumeToolResponse(response rpcEnvelope, tool Tool) error {
	if len(response.Error) > 0 {
		var fields map[string]json.RawMessage
		if decodeObject(response.Error, &fields) != nil || !onlyKeys(fields, "code", "message", "data") {
			return c.Fail(c.PolicyDenied, "extension JSON-RPC error response is malformed")
		}
		var rpcErr rpcError
		if c.DecodeStrict(response.Error, &rpcErr) != nil || rpcErr.Message == "" || len(rpcErr.Message) > 1024 {
			return c.Fail(c.PolicyDenied, "extension JSON-RPC error response is invalid")
		}
		x.toolError = true
		x.errorCode = rpcErr.Code
		x.errorMessage = rpcErr.Message
		return nil
	}
	content, structured, isError, err := parseToolResult(response.Result, tool)
	if err != nil {
		return err
	}
	x.content = content
	x.structured = structured
	x.toolError = isError
	return nil
}

func (x *exchange) callTool(ctx context.Context, tool Tool, arguments json.RawMessage) error {
	params := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{Name: tool.Name, Arguments: bytes.Clone(arguments)}
	response, _, err := x.request(ctx, "tools/call", params)
	if err != nil {
		if errors.Is(err, errRPCErrorResponse) {
			return x.consumeToolResponse(response, tool)
		}
		return err
	}
	return x.consumeToolResponse(response, tool)
}
