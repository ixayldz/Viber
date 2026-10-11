package runner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const duplexChildModeEnv = "VIBER_RUNNER_DUPLEX_CHILD_MODE"

// The test binary acts as a tiny local process so stream lifecycle tests do
// not depend on a running Docker daemon or a shell installed on the host.
func TestDuplexChildProcess(t *testing.T) {
	switch os.Getenv(duplexChildModeEnv) {
	case "exchange":
		_, _ = io.WriteString(os.Stdout, "ready\n")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			os.Exit(2)
		}
		_, _ = io.WriteString(os.Stdout, "echo:"+line)
		_, _ = io.Copy(io.Discard, os.Stdin) // Runner stdin close must deliver EOF.
		os.Exit(0)
	case "overflow":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 8192))
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "wait-eof":
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}

func duplexChild(mode string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestDuplexChildProcess$")
	command.Env = append(os.Environ(), duplexChildModeEnv+"="+mode)
	return command
}

func TestRunDuplexCommandStreamsAndPinsTranscript(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result Result
	var response string
	err := runDuplexCommand(ctx, cancel, duplexChild("exchange"), func(_ context.Context, stdout io.Reader, stdin io.Writer) error {
		reader := bufio.NewReader(stdout)
		ready, err := reader.ReadString('\n')
		if err != nil || ready != "ready\n" {
			return errors.New("missing initialize response")
		}
		if _, err = io.WriteString(stdin, "initialize\n"); err != nil {
			return err
		}
		response, err = reader.ReadString('\n')
		return err
	}, 4096, &result)
	if err != nil {
		t.Fatal(err)
	}
	if response != "echo:initialize\n" || result.Stdout == nil || string(result.Stdout) != "ready\necho:initialize\n" {
		t.Fatalf("duplex exchange or drain incomplete: response=%q stdout=%q", response, result.Stdout)
	}
	if result.DuplexTranscriptDigest == "" || result.DuplexStdinBytes != int64(len("initialize\n")) || result.DuplexStdoutBytes != int64(len(result.Stdout)) {
		t.Fatalf("transcript was not pinned: %+v", result)
	}
}

func TestRunDuplexCommandRejectsInputAndOutputOverruns(t *testing.T) {
	t.Run("input", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var result Result
		err := runDuplexCommand(ctx, cancel, duplexChild("wait-eof"), func(_ context.Context, _ io.Reader, stdin io.Writer) error {
			_, err := stdin.Write(make([]byte, maxDuplexInputBytes+1))
			return err
		}, 4096, &result)
		if !errors.Is(err, errDuplexLimit) || result.DuplexStdinBytes != 0 {
			t.Fatalf("oversize input was accepted: bytes=%d err=%v", result.DuplexStdinBytes, err)
		}
	})
	t.Run("output", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var result Result
		err := runDuplexCommand(ctx, cancel, duplexChild("overflow"), func(_ context.Context, stdout io.Reader, _ io.Writer) error {
			_, err := io.Copy(io.Discard, stdout)
			return err
		}, 1024, &result)
		if !errors.Is(err, errDuplexLimit) || !result.OutputTruncated || len(result.Stdout) != 1024 || result.DuplexStdoutBytes != 1024 {
			t.Fatalf("oversize output was not bounded: bytes=%d truncated=%t err=%v", len(result.Stdout), result.OutputTruncated, err)
		}
	})
}

func TestDuplexInputCloseUnblocksPendingWrite(t *testing.T) {
	reader, pipe := io.Pipe()
	defer reader.Close()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := &duplexInput{pipe: pipe, transcript: newDuplexTranscript(), cancel: cancel}
	done := make(chan error, 1)
	go func() {
		_, err := input.Write([]byte("blocked until cancellation"))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("write unexpectedly completed without a reader: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	input.close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed input reported a successful blocked write")
		}
	case <-time.After(time.Second):
		t.Fatal("closing duplex input did not unblock the pending write")
	}
}
