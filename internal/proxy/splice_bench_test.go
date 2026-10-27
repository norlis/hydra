package proxy

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/norlis/hydra/internal/proxy/conntrack"
)

// tcpPair returns two ends of a loopback TCP connection. Payloads are
// kept at 64 MiB or more so a -count=10 run opens few enough sockets per
// second that TIME_WAIT does not exhaust the ephemeral port range.
func tcpPair(b *testing.B) (dialed, accepted net.Conn) {
	b.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	acceptCh := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(acceptCh)
			return
		}
		acceptCh <- c
	}()
	dialed, err = (&net.Dialer{}).DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	accepted, ok := <-acceptCh
	if !ok {
		b.Fatal("accept failed")
	}
	return dialed, accepted
}

// BenchmarkSplice measures one-directional throughput through splice:
// payload written at the client end, drained at the remote end. The
// remote conn is optionally wrapped like the forwarder does.
func BenchmarkSplice(b *testing.B) {
	cases := []struct {
		name string
		size int
		wrap func(net.Conn) net.Conn
	}{
		{"raw 64MiB", 64 << 20, nil},
		{"instrumentado 64MiB", 64 << 20, func(c net.Conn) net.Conn { return conntrack.WrapWithIdle(c, 0, nil) }},
		{"instrumentado+watchdog 64MiB", 64 << 20, func(c net.Conn) net.Conn { return conntrack.WrapWithIdle(c, time.Minute, nil) }},
		{"instrumentado 256MiB", 256 << 20, func(c net.Conn) net.Conn { return conntrack.WrapWithIdle(c, 0, nil) }},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			payload := make([]byte, 64<<10)
			b.SetBytes(int64(tc.size))
			b.ReportAllocs()
			for b.Loop() {
				clientA, clientB := tcpPair(b)
				remoteA, remoteB := tcpPair(b)
				remote := remoteA
				if tc.wrap != nil {
					remote = tc.wrap(remoteA)
				}
				done := make(chan struct{})
				go func() {
					defer close(done)
					_, _ = io.Copy(io.Discard, remoteB)
				}()
				go func() {
					for sent := 0; sent < tc.size; sent += len(payload) {
						if _, err := clientA.Write(payload); err != nil {
							return
						}
					}
					_ = clientA.Close()
				}()
				splice(clientB, remote, nil, nil)
				_ = remoteB.Close()
				<-done
			}
		})
	}
}
