package runner

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os/exec"
	"sync"
)

const maxDuplexInputBytes = 64 << 10

var errDuplexLimit = errors.New("duplex stream exceeded its byte limit")

type DuplexDriver func(context.Context, io.Reader, io.Writer) error

type duplexDriverKey struct{}

func duplexDriverFromContext(ctx context.Context) (DuplexDriver, bool) {
	if ctx == nil {
		return nil, false
	}
	driver, ok := ctx.Value(duplexDriverKey{}).(DuplexDriver)
	return driver, ok && driver != nil
}

// RunDuplex starts the same owned, pinned, read-only computation as Run, but
// gives a trusted protocol driver bounded live streams for an interactive
// exchange. The driver is part of the trusted host runtime; it must validate
// the peer's initialize and discovery responses before issuing any effectful
// method. The runner only transports bytes and never treats protocol replies
// as verification evidence.
func (d *Docker) RunDuplex(ctx context.Context, source string, p Profile, inv Invocation, driver DuplexDriver) (Result, error) {
	if ctx == nil || driver == nil || inv.Stdin != nil {
		return Result{}, errors.New("duplex run requires a driver and no preloaded stdin")
	}
	empty := []byte{}
	inv.Stdin = &empty // Bind OpenStdin and the interactive invocation in the ownership capsule.
	ctx = context.WithValue(ctx, duplexDriverKey{}, driver)
	return d.Run(ctx, source, p, inv)
}

type duplexTranscript struct {
	mu          sync.Mutex
	input       hash.Hash
	output      hash.Hash
	inputBytes  int64
	outputBytes int64
}

func newDuplexTranscript() *duplexTranscript {
	t := &duplexTranscript{input: sha256.New(), output: sha256.New()}
	_, _ = t.input.Write([]byte("viber:duplex:stdin:v1\x00"))
	_, _ = t.output.Write([]byte("viber:duplex:stdout:v1\x00"))
	return t
}

func (t *duplexTranscript) addInput(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.input.Write(p)
	t.inputBytes += int64(len(p))
}

func (t *duplexTranscript) addOutput(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.output.Write(p)
	t.outputBytes += int64(len(p))
}

func (t *duplexTranscript) result() (string, int64, int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	inputSum := t.input.Sum(nil)
	outputSum := t.output.Sum(nil)
	final := sha256.New()
	_, _ = final.Write([]byte("viber:duplex-transcript:v1\x00"))
	var count [16]byte
	binary.BigEndian.PutUint64(count[:8], uint64(t.inputBytes))
	binary.BigEndian.PutUint64(count[8:], uint64(t.outputBytes))
	_, _ = final.Write(count[:])
	_, _ = final.Write(inputSum)
	_, _ = final.Write(outputSum)
	return hex.EncodeToString(final.Sum(nil)), t.inputBytes, t.outputBytes
}

type duplexInput struct {
	pipe       io.WriteCloser
	transcript *duplexTranscript
	cancel     context.CancelFunc
	used       int64
	writeMu    sync.Mutex
	mu         sync.Mutex
	closeOnce  sync.Once
	overLimit  bool
	closed     bool
}

func (w *duplexInput) Write(p []byte) (int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if w.overLimit {
		w.mu.Unlock()
		return 0, errDuplexLimit
	}
	if int64(len(p)) > maxDuplexInputBytes-w.used {
		w.overLimit = true
		w.mu.Unlock()
		w.cancel()
		return 0, errDuplexLimit
	}
	w.mu.Unlock()
	// Do not hold the state mutex across pipe I/O. Cancellation closes the
	// pipe independently and must be able to unblock a write waiting on Docker.
	n, err := w.pipe.Write(p)
	w.mu.Lock()
	if n > 0 {
		w.used += int64(n)
	}
	w.mu.Unlock()
	if n > 0 {
		w.transcript.addInput(p[:n])
	}
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.cancel()
	}
	return n, err
}

func (w *duplexInput) close() {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		_ = w.pipe.Close()
		// Closing first releases any active Write before waiting for its final
		// byte accounting. Later writes observe closed and never reach the pipe.
		w.writeMu.Lock()
		w.writeMu.Unlock()
	})
}

func (w *duplexInput) exceeded() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.overLimit
}

func runDuplexCommand(ctx context.Context, cancel context.CancelFunc, command *exec.Cmd, driver DuplexDriver, outputLimit int64, result *Result) error {
	if driver == nil || outputLimit <= 0 {
		return errors.New("invalid duplex runner configuration")
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return err
	}
	if err = command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return err
	}

	stdoutCapture := &boundedOutput{limit: outputLimit, onLimit: cancel}
	stderrCapture := &boundedOutput{limit: outputLimit, onLimit: cancel}
	transcript := newDuplexTranscript()
	reader, writer := io.Pipe()
	input := &duplexInput{pipe: stdin, transcript: transcript, cancel: cancel}

	watchStop := make(chan struct{})
	watchDone := make(chan struct{})
	closeOutputReader := sync.Once{}
	closeStdin := input.close
	closeReader := func(readErr error) {
		closeOutputReader.Do(func() {
			if readErr != nil {
				_ = reader.CloseWithError(readErr)
			} else {
				_ = reader.Close()
			}
		})
	}
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			closeReader(ctx.Err())
			closeStdin()
		case <-watchStop:
		}
	}()

	stdoutDone := make(chan error, 1)
	stderrDone := make(chan error, 1)
	go func() {
		stdoutDone <- pumpDuplexStdout(ctx, cancel, stdout, writer, stdoutCapture, transcript, outputLimit)
	}()
	go func() {
		_, copyErr := io.Copy(stderrCapture, stderr)
		stderrDone <- copyErr
	}()

	driverErr := callDuplexDriver(ctx, driver, reader, input)
	closeStdin()
	if input.exceeded() {
		driverErr = errors.Join(driverErr, errDuplexLimit)
	}
	if driverErr != nil {
		cancel()
	}
	closeReader(nil) // Any response tail is still drained and captured by the pump.

	stdoutErr := <-stdoutDone
	stderrErr := <-stderrDone
	waitErr := command.Wait()
	close(watchStop)
	<-watchDone

	result.Stdout = stdoutCapture.Bytes()
	result.Stderr = stderrCapture.Bytes()
	result.OutputTruncated = stdoutCapture.Truncated() || stderrCapture.Truncated()
	result.DuplexTranscriptDigest, result.DuplexStdinBytes, result.DuplexStdoutBytes = transcript.result()
	if result.OutputTruncated {
		driverErr = errors.Join(driverErr, errDuplexLimit)
	}
	if ctx.Err() != nil {
		driverErr = errors.Join(driverErr, ctx.Err())
	}
	// A nonzero process exit is reported by Docker inspect as an outcome. Wait
	// errors are only surfaced when stream collection itself failed.
	_ = waitErr
	return errors.Join(driverErr, stdoutErr, stderrErr)
}

func callDuplexDriver(ctx context.Context, driver DuplexDriver, reader io.Reader, writer io.Writer) (driverErr error) {
	defer func() {
		if recover() != nil {
			driverErr = errors.New("duplex protocol driver failed")
		}
	}()
	return driver(ctx, reader, writer)
}

func pumpDuplexStdout(ctx context.Context, cancel context.CancelFunc, source io.Reader, destination *io.PipeWriter, capture *boundedOutput, transcript *duplexTranscript, limit int64) error {
	defer destination.Close()
	buffer := make([]byte, 32<<10)
	streaming := true
	for {
		n, readErr := source.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			_, _ = capture.Write(chunk)
			remaining := limit - transcriptOutputBytes(transcript)
			if remaining > int64(len(chunk)) {
				remaining = int64(len(chunk))
			}
			if remaining > 0 {
				bounded := chunk[:int(remaining)]
				transcript.addOutput(bounded)
				if streaming {
					written, writeErr := writerAll(destination, bounded)
					if writeErr != nil || written != len(bounded) {
						streaming = false
					}
				}
			}
			if capture.Truncated() {
				cancel()
				if streaming {
					_ = destination.CloseWithError(errDuplexLimit)
					streaming = false
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrClosedPipe) {
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
			return readErr
		}
	}
}

func transcriptOutputBytes(t *duplexTranscript) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.outputBytes
}

func writerAll(writer io.Writer, p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, err := writer.Write(p[total:])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
