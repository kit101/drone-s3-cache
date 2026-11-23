package plugin

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

type gzipArchive struct {
	workspace   string
	destination string
	paths       []string
}

func (a gzipArchive) allowed(name string) bool {
	for _, dir := range a.paths {
		if within(name, dir) {
			return true
		}
	}
	return false
}

func (a gzipArchive) linkTarget(name, link string) (string, error) {
	if path.IsAbs(link) || strings.ContainsAny(link, `\:`) {
		return "", fmt.Errorf("absolute or nonportable archive link")
	}
	target, err := relativePath(path.Join(path.Dir(name), link))
	if err != nil {
		return "", err
	}
	for _, dir := range a.paths {
		if within(name, dir) && within(target, dir) {
			return target, nil
		}
	}
	return "", fmt.Errorf("link escapes its declared cache directory")
}

func (a gzipArchive) Pack(srcs []string, out io.Writer) (result error) {
	started := time.Now()
	defer func() { logrus.WithField("duration", time.Since(started)).Info("cache pack finished") }()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	defer func() { result = errors.Join(result, tw.Close(), gz.Close()) }()
	root, err := os.OpenRoot(a.workspace)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, dir := range srcs {
		if err := safeParents(a.workspace, dir); err != nil {
			return err
		}
		err := filepath.WalkDir(filepath.Join(a.workspace, filepath.FromSlash(dir)), func(filename string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(a.workspace, filename)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if _, err := relativePath(rel); err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				link, err = os.Readlink(filename)
				if err != nil {
					return err
				}
				if _, err := a.linkTarget(rel, link); err != nil {
					return err
				}
				if err := a.checkLink(a.workspace, rel); err != nil {
					return err
				}
			} else if !info.IsDir() && !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported cache file type: %s", rel)
			}
			hdr, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			hdr.Name = rel
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			file, err := root.Open(filepath.FromSlash(rel))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, file)
			return errors.Join(copyErr, file.Close())
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (a gzipArchive) checkLink(base, name string) error {
	resolved, err := filepath.EvalSymlinks(filepath.Join(base, filepath.FromSlash(name)))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(base, resolved)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	for _, dir := range a.paths {
		if within(name, dir) && within(rel, dir) {
			return nil
		}
	}
	return fmt.Errorf("archive link resolves outside its declared directory")
}

func (a gzipArchive) Unpack(_ string, in io.Reader) error {
	started := time.Now()
	defer func() { logrus.WithField("duration", time.Since(started)).Info("cache unpack finished") }()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	root, err := os.OpenRoot(a.destination)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(gz)
	seen := make(map[string]bool)
	content := false
	var dirs, links []*tar.Header
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name, err := relativePath(hdr.Name)
		if err != nil || !a.allowed(name) || seen[name] {
			return fmt.Errorf("invalid, duplicate or undeclared archive path: %s", hdr.Name)
		}
		seen[name] = true
		hdr.Name = name
		if err := root.MkdirAll(filepath.FromSlash(path.Dir(name)), 0755); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(filepath.FromSlash(name), 0755); err != nil {
				return err
			}
			dirs = append(dirs, hdr)
		case tar.TypeReg:
			content = true
			file, err := root.OpenFile(filepath.FromSlash(name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(hdr.Mode)&0777)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, tr)
			if err := errors.Join(copyErr, file.Close()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			content = true
			if _, err := a.linkTarget(name, hdr.Linkname); err != nil {
				return err
			}
			links = append(links, hdr)
		default:
			return fmt.Errorf("unsupported archive entry type: %d", hdr.Typeflag)
		}
	}
	if !content {
		return fmt.Errorf("empty cache archive")
	}
	// Read through the gzip trailer before publishing any staged data.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return err
	}
	for _, hdr := range links {
		if err := root.Symlink(hdr.Linkname, filepath.FromSlash(hdr.Name)); err != nil {
			return err
		}
	}
	for _, hdr := range links {
		if err := a.checkLink(a.destination, hdr.Name); err != nil {
			return err
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := root.Chmod(filepath.FromSlash(dirs[i].Name), fs.FileMode(dirs[i].Mode)&0777); err != nil {
			return err
		}
	}
	for _, dir := range a.paths {
		info, err := root.Lstat(filepath.FromSlash(dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("cache root is not a directory: %s", dir)
		}
	}
	return nil
}

func removeCache(dir string) error {
	// Module caches contain read-only directories; do not follow their links.
	err := filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Chmod(name, 0700)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func publishCache(workspace, stage string, dirs []string) (result error) {
	backup, err := os.MkdirTemp(workspace, ".cache-backup-")
	if err != nil {
		return err
	}
	defer func() {
		if result == nil {
			if err := removeCache(backup); err != nil {
				logrus.WithError(err).Warn("old cache backup cleanup failed")
			}
		}
	}()
	type move struct {
		target, saved     string
		backed, installed bool
	}
	var moves []*move
	defer func() {
		if result == nil {
			return
		}
		for i := len(moves) - 1; i >= 0; i-- {
			m := moves[i]
			if m.installed {
				result = errors.Join(result, removeCache(m.target))
			}
			if m.backed {
				result = errors.Join(result, os.Rename(m.saved, m.target))
			}
		}
		// A failed rollback must retain the backup for recovery.
		if entries, err := os.ReadDir(backup); err == nil && len(entries) == 0 {
			_ = os.Remove(backup)
		} else {
			logrus.WithField("backup", backup).Error("cache rollback incomplete; retained backup")
		}
	}()
	for i, dir := range dirs {
		src := filepath.Join(stage, filepath.FromSlash(dir))
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := safeParents(workspace, dir); err != nil {
			return err
		}
		target := filepath.Join(workspace, filepath.FromSlash(dir))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		m := &move{target: target, saved: filepath.Join(backup, fmt.Sprint(i))}
		moves = append(moves, m)
		if _, err := os.Lstat(target); err == nil {
			if err := os.Rename(target, m.saved); err != nil {
				return err
			}
			m.backed = true
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(src, target); err != nil {
			return err
		}
		m.installed = true
	}
	return nil
}
