package fc

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fake Firecracker API on a Unix socket: checks that requests are well-formed.
func TestClientLoadSnapshot(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "fc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/snapshot/load" {
			http.Error(w, `{"fault_message":"bad route"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	c := NewClient(sock)
	err = c.LoadSnapshot(context.Background(), SnapshotLoad{
		SnapshotPath: "/s/vm.snap",
		MemBackend:   MemBackend{BackendType: "File", BackendPath: "/s/vm.mem"},
		ResumeVM:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mb := got["mem_backend"].(map[string]any)
	if got["snapshot_path"] != "/s/vm.snap" || mb["backend_type"] != "File" || got["resume_vm"] != true {
		t.Fatalf("unexpected body: %v", got)
	}
	if _, ok := got["track_dirty_pages"]; ok {
		t.Fatalf("track_dirty_pages should be omitted when false")
	}

	// Error path surfaces Firecracker's fault message.
	err = c.Pause(context.Background())
	if apiErr, ok := err.(*APIError); !ok || apiErr.Status != 400 || !strings.Contains(apiErr.Body, "bad route") {
		t.Fatalf("expected APIError 400, got %v", err)
	}
}

// Fake Firecracker vsock UDS: speaks the CONNECT handshake, then relays
// the guest agent's reply. Fails the first two attempts to exercise retry.
func TestProbeVsock(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "v.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		n := 0
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n++
			line, _ := bufio.NewReader(c).ReadString('\n')
			if line != "CONNECT 1024\n" {
				c.Close()
				continue
			}
			if n <= 2 { // guest not listening yet: Firecracker just closes
				c.Close()
				continue
			}
			c.Write([]byte("OK 1073741824\nready\n"))
			c.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ProbeVsock(ctx, sock, 1024, time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestPageFaults(t *testing.T) {
	// Any binary works for parsing /proc/<pid>/stat; it may exit quickly
	// because it doesn't understand --api-sock, in which case we skip.
	q, err := Start(ProcessConfig{Binary: "/bin/sh", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Kill()
	minf, _, err := q.PageFaults()
	if err != nil {
		t.Skipf("process exited before stat read: %v", err)
	}
	if minf == 0 {
		t.Fatalf("expected non-zero minor faults")
	}
}
