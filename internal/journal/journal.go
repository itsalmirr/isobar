// Package journal implements a per-source append-only JSONL writer with
// rotation, batched fsync, and a non-blocking enqueue API.
//
// Design contract:
//   - Producers (WS receivers, REST pollers) call Write. Write never blocks;
//     if the internal queue is full the frame is dropped and a counter is
//     incremented. The recorder must never apply backpressure to a market
//     data feed.
//   - A single owner goroutine drains the queue, encodes envelopes to a
//     bufio-backed file, flushes + fsyncs every FlushEvery, and rotates by
//     size or age. On rotate, files are optionally gzipped.
//   - Payload bytes are copied on enqueue so callers may safely reuse their
//     read buffers (this matters for coder/websocket which re-uses the
//     read-buffer between Read calls).
//
// The on-disk shape is stable JSONL: one envelope per line, NDJSON-friendly.
package journal

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wh0o0pz/isobar/internal/clock"
)

// Frame is one record submitted by a producer. Payload should be the raw
// source bytes (e.g., a WS frame body) — not decoded — so that future
// schema changes do not invalidate previously recorded data.
type Frame struct {
	Stamp   clock.Stamp
	Source  string
	Kind    string
	Payload json.RawMessage
}

// envelope is the on-disk JSON shape: flat, compact, NDJSON-friendly.
type envelope struct {
	Wall    int64           `json:"t"`
	Mono    int64           `json:"m"`
	Source  string          `json:"src"`
	Kind    string          `json:"k"`
	Payload json.RawMessage `json:"p,omitempty"`
}

// Options configure the Journal.
type Options struct {
	Dir              string
	Source           string
	MaxFileBytes     int64
	MaxFileAge       time.Duration
	FlushEvery       time.Duration
	QueueSize        int
	CompressOnRotate bool
}

// Journal is a per-source append-only writer with rotation and batched fsync.
type Journal struct {
	opts   Options
	logger *slog.Logger

	in       chan envelope
	closed   chan struct{}
	doneOnce sync.Once
	done     chan struct{}

	dropped atomic.Uint64
}

// Open creates a Journal and starts its writer goroutine. Stop with Close.
func Open(opts Options, logger *slog.Logger) (*Journal, error) {
	if opts.Dir == "" || opts.Source == "" {
		return nil, errors.New("journal: Dir and Source required")
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 8192
	}
	if opts.FlushEvery <= 0 {
		opts.FlushEvery = 250 * time.Millisecond
	}
	if logger == nil {
		logger = slog.Default()
	}
	j := &Journal{
		opts:   opts,
		logger: logger.With("component", "journal", "src", opts.Source),
		in:     make(chan envelope, opts.QueueSize),
		closed: make(chan struct{}),
		done:   make(chan struct{}),
	}
	go j.run()
	return j, nil
}

// Write enqueues a frame. It never blocks; if the queue is full the frame
// is dropped and the dropped counter incremented (a periodic warn log
// surfaces the count).
//
// Payload bytes are copied so callers may reuse their read buffer.
func (j *Journal) Write(f Frame) {
	var p json.RawMessage
	if len(f.Payload) > 0 {
		p = make(json.RawMessage, len(f.Payload))
		copy(p, f.Payload)
	}
	e := envelope{
		Wall:    f.Stamp.Wall,
		Mono:    f.Stamp.Mono,
		Source:  f.Source,
		Kind:    f.Kind,
		Payload: p,
	}
	select {
	case j.in <- e:
	default:
		j.dropped.Add(1)
	}
}

// Dropped returns the number of frames dropped due to a full queue.
func (j *Journal) Dropped() uint64 { return j.dropped.Load() }

// Close stops the writer goroutine, draining the queue and fsyncing the
// current file. Safe to call from any goroutine; idempotent.
func (j *Journal) Close() error {
	j.doneOnce.Do(func() { close(j.closed) })
	<-j.done
	return nil
}

// Wait blocks until the writer goroutine exits or ctx is cancelled.
// Useful in tests; not required in normal use.
func (j *Journal) Wait(ctx context.Context) {
	select {
	case <-j.done:
	case <-ctx.Done():
	}
}

// run is the single owner of the on-disk file.
func (j *Journal) run() {
	defer close(j.done)

	var (
		f       *os.File
		bw      *bufio.Writer
		opened  time.Time
		written int64
		encBuf  bytes.Buffer
	)
	encoder := json.NewEncoder(&encBuf)
	encoder.SetEscapeHTML(false)

	open := func() error {
		path, err := newPath(j.opts.Dir, j.opts.Source)
		if err != nil {
			return err
		}
		nf, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // journal paths are built from our own Dir+timestamp, not user input
		if err != nil {
			return err
		}
		f = nf
		bw = bufio.NewWriterSize(f, 64<<10)
		opened = time.Now()
		written = 0
		j.logger.Info("journal opened", "path", path)
		return nil
	}

	flushAndSync := func() {
		if bw == nil || f == nil {
			return
		}
		if err := bw.Flush(); err != nil {
			j.logger.Error("flush failed", "err", err)
			return
		}
		if err := f.Sync(); err != nil {
			j.logger.Error("fsync failed", "err", err)
		}
	}

	rotate := func() {
		if f == nil {
			return
		}
		flushAndSync()
		oldPath := f.Name()
		_ = f.Close()
		f, bw = nil, nil

		if !j.opts.CompressOnRotate {
			return
		}
		if err := gzipFile(oldPath); err != nil {
			j.logger.Warn("gzip on rotate failed; keeping plain file", "path", oldPath, "err", err)
			return
		}
		if err := os.Remove(oldPath); err != nil {
			j.logger.Warn("remove plain after gzip failed", "path", oldPath, "err", err)
		}
	}

	writeOne := func(e envelope) {
		encBuf.Reset()
		if err := encoder.Encode(&e); err != nil {
			j.logger.Error("encode failed", "err", err)
			return
		}
		// json.Encoder appends a newline already; bw.Write returns the byte count.
		n, err := bw.Write(encBuf.Bytes())
		if err != nil {
			j.logger.Error("write failed", "err", err)
			return
		}
		written += int64(n)
	}

	maybeRotateBySize := func() {
		if j.opts.MaxFileBytes > 0 && written >= j.opts.MaxFileBytes {
			rotate()
			if err := open(); err != nil {
				j.logger.Error("re-open after size rotate failed", "err", err)
			}
		}
	}

	if err := open(); err != nil {
		j.logger.Error("initial open failed; entering drain mode", "err", err)
		<-j.closed
		return
	}

	flushTimer := time.NewTicker(j.opts.FlushEvery)
	defer flushTimer.Stop()
	rotateTimer := time.NewTicker(15 * time.Second)
	defer rotateTimer.Stop()
	dropReport := time.NewTicker(60 * time.Second)
	defer dropReport.Stop()

	for {
		select {
		case <-j.closed:
			// Drain remaining queue, then fsync + close. Rotate as needed:
			// on shutdown bursts the residual queue can otherwise produce a
			// final file that exceeds MaxFileBytes by an unbounded margin.
			for {
				select {
				case e := <-j.in:
					writeOne(e)
					maybeRotateBySize()
				default:
					flushAndSync()
					if f != nil {
						_ = f.Close()
					}
					return
				}
			}

		case e := <-j.in:
			writeOne(e)
			maybeRotateBySize()

		case <-flushTimer.C:
			flushAndSync()

		case <-rotateTimer.C:
			if j.opts.MaxFileAge > 0 && f != nil && time.Since(opened) >= j.opts.MaxFileAge {
				rotate()
				if err := open(); err != nil {
					j.logger.Error("re-open after age rotate failed", "err", err)
					return
				}
			}

		case <-dropReport.C:
			if d := j.dropped.Load(); d > 0 {
				j.logger.Warn("frames dropped (queue full)", "count", d)
			}
		}
	}
}

// newPath builds dir/source/YYYY-MM-DD/source-HHMMSS.NNNNNNNNN.jsonl,
// creating directories as needed.
func newPath(dir, source string) (string, error) {
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	folder := filepath.Join(dir, day)
	if err := os.MkdirAll(folder, 0o750); err != nil {
		return "", err
	}
	stamp := now.Format("150405.000000000")
	return filepath.Join(folder, fmt.Sprintf("%s-%s.jsonl", source, stamp)), nil
}

// gzipFile compresses src into src+".gz". Returns nil on success.
func gzipFile(src string) error {
	in, err := os.Open(src) //nolint:gosec // src is a journal file path we wrote ourselves
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(src+".gz", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // see above
	if err != nil {
		return err
	}
	defer out.Close()

	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}
