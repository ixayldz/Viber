//go:build linux || darwin

package ipc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLongStoreUsesPrivateLocalSocketAndCleansExactEndpoint(t *testing.T) {
	directory := filepath.Join(t.TempDir(), strings.Repeat("a", 100))
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	server, err := OpenServer(context.Background(), directory, 1, func(context.Context, Request) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	address := server.Info.Address
	if filepath.Dir(address) == directory {
		t.Fatal("long path was not relocated")
	}
	info, err := os.Stat(filepath.Dir(address))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("endpoint directory is not private", err)
	}
	socket, err := os.Stat(address)
	if err != nil || socket.Mode().Perm() != 0600 {
		t.Fatal("socket is not private", err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	if _, err := Call(context.Background(), server.Info, Request{ID: "long-path", Command: "status", Payload: []byte("null")}); err != nil {
		t.Fatal(err)
	}
	if err = server.Close(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Dir(address)); !os.IsNotExist(err) {
		t.Fatal("temporary endpoint not removed", err)
	}
	if _, err = os.Stat(directory); err != nil {
		t.Fatal("store directory removed", err)
	}
}
