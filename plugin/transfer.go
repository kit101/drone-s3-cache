package plugin

import (
	"context"
	"fmt"
	"io"

	"github.com/drone/drone-cache-lib/archive"
)

type cacheStorage interface {
	GetContext(context.Context, string, io.Writer) error
	PutContext(context.Context, string, io.Reader) error
}

func rebuildCache(s cacheStorage, a archive.Archive, srcs []string, dst string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := a.Pack(srcs, writer)
		_ = writer.CloseWithError(err)
		done <- err
	}()
	uploadErr := s.PutContext(ctx, dst, reader)
	// Wake a blocked archiver before waiting for it.
	_ = reader.CloseWithError(uploadErr)
	cancel()
	packErr := <-done
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
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := s.GetContext(ctx, src, writer)
		_ = writer.CloseWithError(err)
		done <- err
	}()
	unpackErr := a.Unpack("", reader)
	if unpackErr == nil {
		// Tar may finish before the transport; still check the remaining stream.
		_, unpackErr = io.Copy(io.Discard, reader)
	}
	_ = reader.CloseWithError(unpackErr)
	cancel()
	downloadErr := <-done
	if unpackErr != nil {
		return fmt.Errorf("unpack cache: %w", unpackErr)
	}
	if downloadErr != nil {
		return fmt.Errorf("download cache: %w", downloadErr)
	}
	return nil
}
