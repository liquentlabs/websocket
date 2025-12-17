//go:build !js

package websocket

import (
	"bufio"
	"bytes"
	"compress/flate"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket/internal/test/assert"
	"github.com/coder/websocket/internal/test/xrand"
)

func Test_slidingWindow(t *testing.T) {
	t.Parallel()

	const testCount = 99
	const maxWindow = 99999
	for range testCount {
		t.Run("", func(t *testing.T) {
			t.Parallel()

			input := xrand.String(maxWindow)
			windowLength := xrand.Int(maxWindow)
			var sw slidingWindow
			sw.init(windowLength)
			sw.write([]byte(input))

			assert.Equal(t, "window length", windowLength, cap(sw.buf))
			if !strings.HasSuffix(input, string(sw.buf)) {
				t.Fatalf("r.buf is not a suffix of input: %q and %q", input, sw.buf)
			}
		})
	}
}

func BenchmarkFlateWriter(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w, _ := flate.NewWriter(io.Discard, flate.BestSpeed)
		// We have to write a byte to get the writer to allocate to its full extent.
		w.Write([]byte{'a'})
		w.Flush()
	}
}

func BenchmarkFlateReader(b *testing.B) {
	b.ReportAllocs()

	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestSpeed)
	w.Write([]byte{'a'})
	w.Flush()

	for i := 0; i < b.N; i++ {
		r := flate.NewReader(bytes.NewReader(buf.Bytes()))
		io.ReadAll(r)
	}
}

// TestWriteSingleFrameCompressed verifies that Conn.Write sends compressed
// messages in a single frame instead of multiple frames, and that messages
// below the flateThreshold are sent uncompressed.
// This is a regression test for https://github.com/coder/websocket/issues/435
func TestWriteSingleFrameCompressed(t *testing.T) {
	t.Parallel()

	var (
		flateThreshold = 64

		largeMsg = []byte(strings.Repeat("hello world ", 100)) // ~1200 bytes, above threshold
		smallMsg = []byte("small message")                     // 13 bytes, below threshold
	)

	testCases := []struct {
		name     string
		mode     CompressionMode
		msg      []byte
		wantRsv1 bool // true = compressed, false = uncompressed
	}{
		{"ContextTakeover/AboveThreshold", CompressionContextTakeover, largeMsg, true},
		{"NoContextTakeover/AboveThreshold", CompressionNoContextTakeover, largeMsg, true},
		{"ContextTakeover/BelowThreshold", CompressionContextTakeover, smallMsg, false},
		{"NoContextTakeover/BelowThreshold", CompressionNoContextTakeover, smallMsg, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()

			c := newConn(connConfig{
				rwc:            clientConn,
				client:         true,
				copts:          tc.mode.opts(),
				flateThreshold: flateThreshold,
				br:             bufio.NewReader(clientConn),
				bw:             bufio.NewWriterSize(clientConn, 4096),
			})

			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
			defer cancel()

			writeDone := make(chan error, 1)
			go func() {
				writeDone <- c.Write(ctx, MessageText, tc.msg)
			}()

			reader := bufio.NewReader(serverConn)
			readBuf := make([]byte, 8)

			h, err := readFrameHeader(reader, readBuf)
			assert.Success(t, err)

			_, err = io.CopyN(io.Discard, reader, h.payloadLength)
			assert.Success(t, err)

			assert.Equal(t, "opcode", opText, h.opcode)
			assert.Equal(t, "rsv1 (compressed)", tc.wantRsv1, h.rsv1)
			assert.Equal(t, "fin", true, h.fin)

			err = <-writeDone
			assert.Success(t, err)
		})
	}
}

// TestWriteThenWriterContextTakeover verifies that using Conn.Write followed by
// Conn.Writer works correctly with context takeover enabled. This tests that
// the flateWriter destination is properly restored after Conn.Write redirects
// it to a temporary buffer.
func TestWriteThenWriterContextTakeover(t *testing.T) {
	t.Parallel()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := newConn(connConfig{
		rwc:            clientConn,
		client:         true,
		copts:          CompressionContextTakeover.opts(),
		flateThreshold: 64,
		br:             bufio.NewReader(clientConn),
		bw:             bufio.NewWriterSize(clientConn, 4096),
	})

	server := newConn(connConfig{
		rwc:            serverConn,
		client:         false,
		copts:          CompressionContextTakeover.opts(),
		flateThreshold: 64,
		br:             bufio.NewReader(serverConn),
		bw:             bufio.NewWriterSize(serverConn, 4096),
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*500)
	defer cancel()

	msg1 := []byte(strings.Repeat("first message ", 100))
	msg2 := []byte(strings.Repeat("second message ", 100))

	type readResult struct {
		typ MessageType
		p   []byte
		err error
	}
	readCh := make(chan readResult, 2)
	go func() {
		for range 2 {
			typ, p, err := server.Read(ctx)
			readCh <- readResult{typ, p, err}
		}
	}()

	// First message: Write() redirects flateWriter to temp buffer
	assert.Success(t, client.Write(ctx, MessageText, msg1))

	r := <-readCh
	assert.Success(t, r.err)
	assert.Equal(t, "msg1 type", MessageText, r.typ)
	assert.Equal(t, "msg1 content", string(msg1), string(r.p))

	// Second message: Writer() streaming API
	w, err := client.Writer(ctx, MessageBinary)
	assert.Success(t, err)
	_, err = w.Write(msg2)
	assert.Success(t, err)
	assert.Success(t, w.Close())

	r = <-readCh
	assert.Success(t, r.err)
	assert.Equal(t, "msg2 type", MessageBinary, r.typ)
	assert.Equal(t, "msg2 content", string(msg2), string(r.p))
}
