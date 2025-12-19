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

		largeMsg = []byte(strings.Repeat("hello world ", 100))
		smallMsg = []byte("small message")
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

	// 2. `Write` API
	err := client.Write(ctx, MessageText, msg1)
	assert.Success(t, err)

	r := <-readCh
	assert.Success(t, r.err)
	assert.Equal(t, "msg1 type", MessageText, r.typ)
	assert.Equal(t, "msg1 content", string(msg1), string(r.p))

	// 2. `Writer` API
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

// TestCompressionDictionaryPreserved verifies that context takeover mode
// preserves the compression dictionary across Conn.Write calls, resulting
// in better compression for consecutive similar messages.
func TestCompressionDictionaryPreserved(t *testing.T) {
	t.Parallel()

	msg := []byte(strings.Repeat(`{"type":"event","data":"value"}`, 50))

	// Test with context takeover
	clientConn1, serverConn1 := net.Pipe()
	defer clientConn1.Close()
	defer serverConn1.Close()

	withTakeover := newConn(connConfig{
		rwc:            clientConn1,
		client:         true,
		copts:          CompressionContextTakeover.opts(),
		flateThreshold: 64,
		br:             bufio.NewReader(clientConn1),
		bw:             bufio.NewWriterSize(clientConn1, 4096),
	})

	// Test without context takeover
	clientConn2, serverConn2 := net.Pipe()
	defer clientConn2.Close()
	defer serverConn2.Close()

	withoutTakeover := newConn(connConfig{
		rwc:            clientConn2,
		client:         true,
		copts:          CompressionNoContextTakeover.opts(),
		flateThreshold: 64,
		br:             bufio.NewReader(clientConn2),
		bw:             bufio.NewWriterSize(clientConn2, 4096),
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Capture compressed sizes for both modes
	var withTakeoverSizes, withoutTakeoverSizes []int64

	reader1 := bufio.NewReader(serverConn1)
	reader2 := bufio.NewReader(serverConn2)
	readBuf := make([]byte, 8)

	// Send 3 identical messages each
	for range 3 {
		// With context takeover
		writeDone1 := make(chan error, 1)
		go func() {
			writeDone1 <- withTakeover.Write(ctx, MessageText, msg)
		}()

		h1, err := readFrameHeader(reader1, readBuf)
		assert.Success(t, err)

		_, err = io.CopyN(io.Discard, reader1, h1.payloadLength)
		assert.Success(t, err)

		withTakeoverSizes = append(withTakeoverSizes, h1.payloadLength)
		assert.Success(t, <-writeDone1)

		// Without context takeover
		writeDone2 := make(chan error, 1)
		go func() {
			writeDone2 <- withoutTakeover.Write(ctx, MessageText, msg)
		}()

		h2, err := readFrameHeader(reader2, readBuf)
		assert.Success(t, err)

		_, err = io.CopyN(io.Discard, reader2, h2.payloadLength)
		assert.Success(t, err)

		withoutTakeoverSizes = append(withoutTakeoverSizes, h2.payloadLength)
		assert.Success(t, <-writeDone2)
	}

	// With context takeover, the 2nd and 3rd messages should be smaller
	// than without context takeover (dictionary helps compress repeated patterns).
	// The first message will be similar size for both modes since there's no
	// prior dictionary. But subsequent messages benefit from context takeover.
	if withTakeoverSizes[2] >= withoutTakeoverSizes[2] {
		t.Errorf("context takeover should compress better: with=%d, without=%d",
			withTakeoverSizes[2], withoutTakeoverSizes[2])
	}
}
