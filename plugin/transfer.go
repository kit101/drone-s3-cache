package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/drone/drone-cache-lib/archive"
)

type cacheStorage interface {
	Get(context.Context, string) (io.ReadCloser, error)
	Put(context.Context, string, io.Reader) error
}

// Track errors from final tar/gzip writes that cache-lib's Pack may discard.
type checkedWriter struct {
	io.Writer
	err error
}

func (w *checkedWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if w.err == nil {
		w.err = err
	}
	return n, err
}

func rebuildCache(s cacheStorage, a archive.Archive, srcs []string, dst string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		checked := &checkedWriter{Writer: writer}
		err := a.Pack(srcs, checked)
		if err == nil {
			err = checked.err
		}
		_ = writer.CloseWithError(err)
		done <- err
	}()
	uploadErr := s.Put(ctx, dst, reader)
	// Wake the archiver before waiting, even if the uploader never read the pipe.
	_ = reader.CloseWithError(uploadErr)
	cancel()
	packErr := <-done
	if packErr != nil && !errors.Is(packErr, uploadErr) && !errors.Is(packErr, io.ErrClosedPipe) {
		return errors.Join(fmt.Errorf("pack cache: %w", packErr), uploadErr)
	}
	if uploadErr != nil {
		return fmt.Errorf("upload cache: %w", uploadErr)
	}
	if packErr != nil {
		return fmt.Errorf("pack cache: %w", packErr)
	}
	return nil
}

func restoreCache(s cacheStorage, a archive.Archive, src string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, err := s.Get(ctx, src)
	if err != nil {
		return fmt.Errorf("download cache: %w", err)
	}
	err = a.Unpack("", body)
	if err == nil {
		_, err = io.Copy(io.Discard, body)
	}
	closeErr := body.Close()
	if err != nil {
		return fmt.Errorf("unpack cache: %w", err)
	}
	return closeErr
}
