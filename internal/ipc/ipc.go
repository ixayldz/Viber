// Package ipc provides bounded, peer-authenticated local owner RPC. The caller
// must already hold the store owner lock before publishing a server endpoint.
package ipc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

const MaxFrame = 8 << 20

var ErrNoOwner = errors.New("no owner endpoint")

type Info struct {
	SchemaVersion int    `json:"schema_version"`
	Generation    int64  `json:"kernel_generation"`
	PID           int    `json:"owner_pid"`
	ID            string `json:"owner_id"`
	Transport     string `json:"transport"`
	Address       string `json:"address"`
}
type Request struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"request_id"`
	OwnerID       string          `json:"owner_id"`
	Generation    int64           `json:"kernel_generation"`
	Command       string          `json:"command"`
	TaskID        string          `json:"task_id"`
	Payload       json.RawMessage `json:"payload"`
}
type Response struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"request_id"`
	OwnerID       string          `json:"owner_id"`
	Generation    int64           `json:"kernel_generation"`
	Result        json.RawMessage `json:"result"`
	Error         *c.Error        `json:"error"`
}
type Handler func(context.Context, Request) (any, error)
type CallError struct {
	Dispatched bool
	Cause      error
}

func (e *CallError) Error() string {
	if e.Dispatched {
		return "owner response unavailable; operation outcome requires reconciliation"
	}
	return "owner connection unavailable"
}
func (e *CallError) Unwrap() error { return e.Cause }

type Server struct {
	Info        Info
	root        *os.Root
	listener    net.Listener
	handler     Handler
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	connections map[net.Conn]bool
	closed      bool
	slots       chan struct{}
	wg          sync.WaitGroup
}

func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
func validateInfo(info Info) error {
	if info.SchemaVersion != 1 || info.Generation < 1 || info.PID < 1 || len(info.ID) != 32 {
		return c.Fail(c.StoreIntegrityError, "invalid owner descriptor")
	}
	if _, err := hex.DecodeString(info.ID); err != nil {
		return c.Fail(c.StoreIntegrityError, "invalid owner identity")
	}
	return validateAddress(info)
}
func OpenServer(ctx context.Context, directory string, generation int64, handler Handler) (*Server, error) {
	if generation < 1 || handler == nil {
		return nil, c.Fail(c.InvalidArgument, "owner generation and handler required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			root.Close()
		}
	}()
	if err = fileguard.Private(root); err != nil {
		return nil, err
	}
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	info := Info{SchemaVersion: 1, Generation: generation, PID: os.Getpid(), ID: id}
	listener, err := listen(root, &info)
	if err != nil {
		return nil, err
	}
	defer func() {
		if failed {
			listener.Close()
		}
	}()
	if err = validateInfo(info); err != nil {
		return nil, err
	}
	// Rotation is allowed only under the caller's already acquired owner lock.
	// Crash leftovers are not proof of an active owner.
	if _, err = root.Lstat("owner.json"); err == nil {
		if _, err = fileguard.ReadRegular(root, "owner.json", 4096); err != nil {
			return nil, err
		}
		if err = root.Remove("owner.json"); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw, err := c.CanonicalV1(info)
	if err != nil {
		return nil, err
	}
	if err = fileguard.Publish(root, "owner.json", raw); err != nil {
		return nil, err
	}
	serverCtx, cancel := context.WithCancel(ctx)
	server := &Server{Info: info, root: root, listener: listener, handler: handler, ctx: serverCtx, cancel: cancel, connections: map[net.Conn]bool{}, slots: make(chan struct{}, 32)}
	failed = false
	return server, nil
}
func Discover(directory string) (Info, error) {
	var info Info
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return info, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return info, ErrNoOwner
		}
		return info, err
	}
	defer root.Close()
	raw, err := fileguard.ReadRegular(root, "owner.json", 4096)
	if errors.Is(err, os.ErrNotExist) {
		return info, ErrNoOwner
	}
	if err != nil {
		return info, err
	}
	if err = c.DecodeStrict(raw, &info); err != nil {
		return info, err
	}
	return info, validateInfo(info)
}
func (s *Server) Serve() error {
	go func() { <-s.ctx.Done(); s.listener.Close() }()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if err = authenticate(conn, s.Info, true); err != nil {
			conn.Close()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return nil
		}
		select {
		case s.slots <- struct{}{}:
			s.connections[conn] = true
			s.wg.Add(1)
			s.mu.Unlock()
			go s.serveConnection(conn)
		default:
			s.mu.Unlock()
			conn.Close()
		}
	}
}
func (s *Server) serveConnection(conn net.Conn) {
	defer func() {
		conn.Close()
		s.mu.Lock()
		delete(s.connections, conn)
		s.mu.Unlock()
		<-s.slots
		s.wg.Done()
	}()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	raw, err := readFrame(conn)
	if err != nil {
		return
	}
	var request Request
	if err = c.DecodeStrict(raw, &request); err != nil {
		return
	}
	response := Response{SchemaVersion: 1, ID: request.ID, OwnerID: s.Info.ID, Generation: s.Info.Generation, Result: json.RawMessage("null")}
	if request.SchemaVersion != 1 || request.OwnerID != s.Info.ID || request.Generation != s.Info.Generation || request.ID == "" || len(request.ID) > 128 || request.Command == "" {
		response.Error = &c.Error{Code: c.StaleAuthority, Message: "owner/request binding mismatch"}
	} else {
		conn.SetReadDeadline(time.Time{})
		ctx, cancel := context.WithTimeout(s.ctx, 15*time.Minute)
		result, handlerErr := s.handler(ctx, request)
		cancel()
		if handlerErr != nil {
			var typed *c.Error
			if errors.As(handlerErr, &typed) {
				response.Error = typed
			} else {
				response.Error = &c.Error{Code: c.StoreIntegrityError, Message: "owner command failed"}
			}
		} else {
			response.Result, err = c.CanonicalV1(result)
			if err != nil {
				response.Error = &c.Error{Code: c.StoreIntegrityError, Message: "owner result encoding failed"}
				response.Result = json.RawMessage("null")
			}
		}
	}
	raw, err = c.CanonicalV1(response)
	if err != nil {
		return
	}
	if len(raw) > MaxFrame {
		response.Result = json.RawMessage("null")
		response.Error = &c.Error{Code: c.UnknownOutcome, Message: "owner result exceeds transport quota; inspect task and reconcile the recorded command"}
		raw, err = c.CanonicalV1(response)
		if err != nil {
			return
		}
	}
	conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_ = writeFrame(conn, raw)
}
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	listenerErr := s.listener.Close()
	if errors.Is(listenerErr, net.ErrClosed) {
		listenerErr = nil
	}
	for conn := range s.connections {
		conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	raw, err := fileguard.ReadRegular(s.root, "owner.json", 4096)
	if err == nil {
		var info Info
		if c.DecodeStrict(raw, &info) == nil && info.ID == s.Info.ID {
			err = s.root.Remove("owner.json")
			if err == nil {
				err = fileguard.SyncParents(s.root, ".")
			}
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return errors.Join(listenerErr, err, s.root.Close())
}
func Call(ctx context.Context, info Info, request Request) (Response, error) {
	var response Response
	if err := validateInfo(info); err != nil {
		return response, err
	}
	request.SchemaVersion = 1
	request.OwnerID = info.ID
	request.Generation = info.Generation
	raw, err := c.CanonicalV1(request)
	if err != nil {
		return response, err
	}
	if len(raw) > MaxFrame {
		return response, c.Fail(c.InvalidArgument, "IPC request exceeds transport quota")
	}
	connectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	conn, err := dial(connectCtx, info)
	cancel()
	if err != nil {
		return response, &CallError{Cause: err}
	}
	defer conn.Close()
	if err = authenticate(conn, info, false); err != nil {
		return response, &CallError{Cause: err}
	}
	deadline := time.Now().Add(15 * time.Minute)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	// Once writing starts, a lost response is conservatively an unknown outcome.
	if err = writeFrame(conn, raw); err != nil {
		return response, &CallError{Dispatched: true, Cause: err}
	}
	raw, err = readFrame(conn)
	if err != nil {
		return response, &CallError{Dispatched: true, Cause: err}
	}
	if err = c.DecodeStrict(raw, &response); err != nil {
		return response, &CallError{Dispatched: true, Cause: err}
	}
	if response.SchemaVersion != 1 || response.ID != request.ID || response.OwnerID != info.ID || response.Generation != info.Generation {
		return response, &CallError{Dispatched: true, Cause: c.Fail(c.StaleAuthority, "owner response binding mismatch")}
	}
	if response.Error != nil {
		return response, response.Error
	}
	return response, nil
}
func writeFrame(out io.Writer, raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxFrame {
		return c.Fail(c.InvalidArgument, "IPC frame exceeds quota")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	for _, part := range [][]byte{header[:], raw} {
		for len(part) > 0 {
			n, err := out.Write(part)
			if err != nil {
				return err
			}
			if n <= 0 {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}
func readFrame(in io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(in, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > MaxFrame {
		return nil, c.Fail(c.InvalidArgument, "IPC frame exceeds quota")
	}
	raw := make([]byte, length)
	_, err := io.ReadFull(in, raw)
	return raw, err
}
func socketPath(root *os.Root, id string) string {
	return filepath.Join(root.Name(), "o-"+id[:12]+".sock")
}
