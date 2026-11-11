package fc

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// ProbeVsock connects to a guest vsock port through Firecracker's host-side
// Unix socket (the vsock uds_path) using the "CONNECT <port>" handshake, and
// waits for the guest agent to send a line starting with "ready".
//
// It retries until ctx expires: right after restore the UDS may not exist yet,
// and the guest may not have re-armed its listener.
func ProbeVsock(ctx context.Context, udsPath string, port uint32, poll time.Duration) error {
	var lastErr error
	for {
		if err := probeOnce(udsPath, port); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("vsock probe: %w (last error: %v)", ctx.Err(), lastErr)
		case <-time.After(poll):
		}
	}
}

func probeOnce(udsPath string, port uint32) error {
	c, err := net.DialTimeout("unix", udsPath, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := fmt.Fprintf(c, "CONNECT %d\n", port); err != nil {
		return err
	}
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	if !strings.HasPrefix(line, "OK ") {
		return fmt.Errorf("handshake: unexpected %q", strings.TrimSpace(line))
	}
	line, err = r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading agent reply: %w", err)
	}
	if !strings.HasPrefix(line, "ready") {
		return fmt.Errorf("agent: unexpected %q", strings.TrimSpace(line))
	}
	return nil
}
