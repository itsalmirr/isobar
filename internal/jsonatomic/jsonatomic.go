// Package jsonatomic writes indented JSON to a file atomically.
//
// The calibration tools (cmd/calibrate-fit, cmd/calibrate-bayesian) each
// used to hand-roll the same create-tmp / encode-indented / rename dance,
// and had already drifted apart on cleanup: one removed the tmp file on an
// encode error but leaked it on a Close error, the other cleaned up on
// both, and only one created the parent directory. WriteIndent is the
// single implementation both call — a temp file plus rename so a reader
// never observes a half-written file (ADR-0007's atomic-write property),
// with the tmp file removed on every error path including Close.
package jsonatomic

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// WriteIndent marshals v as 2-space-indented JSON and writes it to path
// atomically: it creates the parent directory, encodes into path+".tmp",
// fsync-free renames the tmp file over path, and removes the tmp file on
// any error (encode or Close). A successful call guarantees path either
// holds the complete new document or is untouched.
func WriteIndent(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // path is operator-supplied output flag
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
