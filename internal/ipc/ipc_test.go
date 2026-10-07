package ipc

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
)

func serverFixture(t *testing.T, handler Handler) (*Server, string) {
	t.Helper()
	root, err := os.MkdirTemp("", "vi-")
	if err != nil {
		t.Fatal(err)
	}
	// Only exact known empty/owned IPC paths are removed; no recursive cleanup.
	server, err := OpenServer(context.Background(), root, 7, handler)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server did not drain")
		}
		_ = os.Remove(root)
	})
	return server, root
}
func TestAuthenticatedLocalOwnerRoundTripAndStaleBindings(t *testing.T) {
	var calls atomic.Int64
	server, root := serverFixture(t, func(ctx context.Context, r Request) (any, error) {
		calls.Add(1)
		return map[string]string{"command": r.Command}, nil
	})
	info, err := Discover(root)
	if err != nil || info != server.Info {
		t.Fatal(info, err)
	}
	response, err := Call(context.Background(), info, Request{ID: "request", Command: "status", TaskID: "task", Payload: json.RawMessage("null")})
	if err != nil || response.ID != "request" || calls.Load() != 1 {
		t.Fatal(response, err)
	}
	wrong := info
	wrong.PID++
	if _, err = Call(context.Background(), wrong, Request{ID: "wrong-pid", Command: "status", Payload: json.RawMessage("null")}); err == nil {
		t.Fatal("server PID spoof accepted")
	}
	// Raw peer-authenticated clients still cannot bypass descriptor/generation.
	conn, err := dial(context.Background(), info)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := Request{SchemaVersion: 1, ID: "stale", OwnerID: info.ID, Generation: info.Generation - 1, Command: "status", Payload: json.RawMessage("null")}
	raw, _ := c.CanonicalV1(request)
	if err = writeFrame(conn, raw); err != nil {
		t.Fatal(err)
	}
	raw, err = readFrame(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.DecodeStrict(raw, &response); err != nil || response.Error == nil || response.Error.Code != c.StaleAuthority || calls.Load() != 1 {
		t.Fatal("stale request executed", err, response)
	}
}
func TestFramingBoundsMalformedRequestAndUnknownResponse(t *testing.T) {
	for _, length := range []uint32{0, MaxFrame + 1, ^uint32(0)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], length)
		if _, err := readFrame(bytes.NewReader(header[:])); err == nil {
			t.Fatal("unbounded frame accepted")
		}
	}
	if err := writeFrame(&bytes.Buffer{}, make([]byte, MaxFrame+1)); err == nil {
		t.Fatal("large write admitted")
	}
	if _, err := readFrame(bytes.NewReader([]byte{0, 0, 0, 3, 1})); err == nil {
		t.Fatal("partial frame accepted")
	}
	var calls atomic.Int64
	server, _ := serverFixture(t, func(ctx context.Context, r Request) (any, error) { calls.Add(1); <-ctx.Done(); return nil, ctx.Err() })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := Call(ctx, server.Info, Request{ID: "slow", Command: "wait", Payload: json.RawMessage("null")})
	var callError *CallError
	if !errors.As(err, &callError) || !callError.Dispatched || calls.Load() != 1 {
		t.Fatal("lost response not unknown", err, calls.Load())
	}
	for _, raw := range [][]byte{[]byte(`{"schema_version":1,"schema_version":1}`), []byte(`{"unrecognized":true}`)} {
		conn, err := dial(context.Background(), server.Info)
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		_ = writeFrame(conn, raw)
		if _, err = readFrame(conn); err == nil {
			t.Fatal("malformed request produced accepted response")
		}
		conn.Close()
	}
	if calls.Load() != 1 {
		t.Fatal("malformed input dispatched")
	}
}
func TestDescriptorRotationAndCloseDoNotRemoveNewOwner(t *testing.T) {
	first, root := serverFixture(t, func(context.Context, Request) (any, error) { return nil, nil })
	other, err := OpenServer(context.Background(), root, 8, func(context.Context, Request) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	// This intentionally simulates replacement under a caller-controlled owner
	// fixture. Real use must hold the store lock; old close must preserve new info.
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := Discover(root)
	if err != nil || info.ID != other.Info.ID {
		t.Fatal("old close removed successor descriptor", err)
	}
	if err = other.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Discover(root); !errors.Is(err, ErrNoOwner) {
		t.Fatal("closed endpoint still discoverable", err)
	}
}
func TestRejectsNonLocalTransportBeforeDial(t *testing.T) {
	info := Info{SchemaVersion: 1, Generation: 1, PID: os.Getpid(), ID: "0123456789abcdef0123456789abcdef", Transport: "tcp", Address: "127.0.0.1:1234"}
	if _, err := Call(context.Background(), info, Request{}); err == nil {
		t.Fatal("TCP owner accepted")
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if err := authenticate(left, Info{}, true); err == nil {
		t.Fatal("unauthenticated transport accepted")
	}
}

func TestOversizedCommandIsRejectedBeforeDispatch(t *testing.T) {
	var calls atomic.Int64
	server, _ := serverFixture(t, func(context.Context, Request) (any, error) { calls.Add(1); return nil, nil })
	raw, _ := json.Marshal(strings.Repeat("x", MaxFrame))
	_, err := Call(context.Background(), server.Info, Request{ID: "too-large", Command: "status", Payload: raw})
	var typed *c.Error
	var dispatched *CallError
	if !errors.As(err, &typed) || typed.Code != c.InvalidArgument || errors.As(err, &dispatched) || calls.Load() != 0 {
		t.Fatal("oversize command was dispatched or marked unknown", err)
	}
}
