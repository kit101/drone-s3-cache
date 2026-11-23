// Copyright (c) 2020, the Drone Plugins project authors.
// Please see the AUTHORS file for details. All rights reserved.
// Use of this source code is governed by an Apache 2.0 license that can be
// found in the LICENSE file.

package plugin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/smithy-go"
	"github.com/drone-plugins/drone-s3-cache/storage/s3"
	"github.com/sirupsen/logrus"
)

const (
	restoreMode = "restore"
	rebuildMode = "rebuild"
	flushMode   = "flush"
)

type storage interface {
	cacheStorage
	List(context.Context, string) ([]s3.FileEntry, error)
	Delete(context.Context, string) error
}

func (p *Plugin) Validate() error {
	s := &p.settings
	if s.Mode != restoreMode && s.Mode != rebuildMode && s.Mode != flushMode {
		return fmt.Errorf("mode must be restore, rebuild or flush")
	}
	if s.Mode != flushMode {
		var err error
		p.entries, err = parseCache(s.Cache)
		if err != nil {
			return err
		}
		if s.FlushPathSet || s.FlushAgeSet {
			return fmt.Errorf("flush-path and flush-age are only valid in flush mode")
		}
		if len(p.entries) == 0 {
			return nil
		}
	} else if s.CacheSet || s.Cache != "" {
		return fmt.Errorf("flush does not accept cache")
	}
	if err := validKey(s.Bucket); err != nil || strings.ContainsAny(s.Bucket, " :") {
		return fmt.Errorf("bucket must be a bucket name without a path")
	}
	owner, err := relativePath(s.Owner)
	if err != nil || strings.ContainsAny(owner, "*?[]{}") {
		return fmt.Errorf("repo.owner must be a relative repository namespace")
	}
	if err := validKey(s.Repo); err != nil || strings.ContainsAny(s.Repo, "*?[]{}:") {
		return fmt.Errorf("repo.name must be a repository name")
	}
	s.Owner = owner
	p.workspace, err = os.Getwd()
	if err != nil {
		return err
	}
	if s.Mode == flushMode {
		if s.FlushAge <= 0 {
			return fmt.Errorf("flush-age must be positive")
		}
		base := s.Owner + "/" + s.Repo + "/"
		if s.FlushPath == "" {
			s.FlushPath = base
		}
		if _, err := relativePath(s.FlushPath); err != nil || !strings.HasPrefix(s.FlushPath, base) {
			return fmt.Errorf("flush-path must stay within %s", base)
		}
	} else {
		if err := validateEntries(p.entries, p.workspace); err != nil {
			return err
		}
		if err := resolveKeys(p.entries, p.workspace, s.KeyFromGit, s.CommitSHA); err != nil {
			return err
		}
	}
	return p.validateS3()
}

func (p *Plugin) validateS3() error {
	opts := &p.settings.S3Options
	u, err := url.Parse(opts.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("endpoint must be an HTTP/HTTPS service URL without bucket, credentials, query or path")
	}
	if opts.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if opts.AddressingStyle != "" && opts.AddressingStyle != "auto" && opts.AddressingStyle != "path" && opts.AddressingStyle != "virtual" {
		return fmt.Errorf("addressing-style must be auto, path or virtual")
	}
	if (opts.Access == "") != (opts.Secret == "") {
		return fmt.Errorf("access-key and secret-key must be provided together")
	}
	if opts.FileCredentials != "" {
		if opts.Access != "" {
			return fmt.Errorf("static credentials and file-credentials cannot be combined")
		}
		if info, err := os.Stat(opts.FileCredentials); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("file-credentials must refer to an existing regular file")
		}
	}
	if opts.AcceleratedEndpoint != "" && (strings.ContainsAny(opts.AcceleratedEndpoint, "/:@?#") || strings.ContainsAny(opts.AcceleratedEndpoint, " \t\n")) {
		return fmt.Errorf("accelerated-endpoint must be a hostname")
	}
	return nil
}

func (p *Plugin) object(key string) string {
	return p.settings.Bucket + "/" + p.settings.Owner + "/" + p.settings.Repo + "/" + key + ".tar.gz"
}

func (p *Plugin) Execute() error {
	if p.settings.Mode != flushMode && len(p.entries) == 0 {
		logrus.Info("cache explicitly disabled; skipping storage")
		return nil
	}
	st, err := s3.New(&p.settings.S3Options)
	if err != nil {
		return err
	}
	return p.execute(st)
}

func (p *Plugin) execute(st storage) error {
	if p.settings.Mode == flushMode {
		prefix := p.settings.Bucket + "/" + p.settings.FlushPath
		files, err := st.List(context.Background(), prefix)
		if err != nil {
			return err
		}
		for _, file := range files {
			if !strings.HasPrefix(file.Path, prefix) {
				return fmt.Errorf("storage returned an object outside the cleanup prefix")
			}
			if file.LastModified.IsZero() {
				return fmt.Errorf("storage returned an object without a last-modified time")
			}
		}
		cutoff := time.Now().AddDate(0, 0, -p.settings.FlushAge)
		for _, file := range files {
			if file.LastModified.Before(cutoff) {
				if err := st.Delete(context.Background(), file.Path); err != nil {
					return err
				}
				logrus.WithField("object", file.Path).Info("expired cache deleted")
			}
		}
		return nil
	}
	for _, entry := range p.entries {
		if p.settings.Mode == restoreMode {
			p.restore(st, entry)
			continue
		}
		var dirs []string
		content := false
		for _, dir := range entry.Paths {
			if err := safeParents(p.workspace, dir); err != nil {
				return err
			}
			src := filepath.Join(p.workspace, filepath.FromSlash(dir))
			if _, err := os.Stat(src); os.IsNotExist(err) {
				logrus.WithField("directory", dir).Warn("cache directory missing; skipping")
				continue
			} else if err != nil {
				return err
			}
			err := filepath.WalkDir(src, func(_ string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					content = true
				}
				return nil
			})
			if err != nil {
				return err
			}
			dirs = append(dirs, dir)
		}
		if !content {
			logrus.WithField("key", entry.resolved).Info("no cache content; skipping upload")
			continue
		}
		archive := gzipArchive{workspace: p.workspace, paths: entry.Paths}
		if err := rebuildCache(st, archive, dirs, p.object(entry.resolved)); err != nil {
			return err
		}
		logrus.WithField("key", entry.resolved).Info("cache uploaded")
	}
	return nil
}

func (p *Plugin) restore(st storage, entry cacheEntry) {
	keys := append([]string{entry.resolved}, entry.FallbackKeys...)
	seen := make(map[string]bool)
	for i, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		stage, err := os.MkdirTemp(p.workspace, ".cache-restore-")
		if err != nil {
			logrus.WithError(err).Warn("cannot stage cache; continuing cold")
			return
		}
		archive := gzipArchive{workspace: p.workspace, destination: stage, paths: entry.Paths}
		err = restoreCache(st, archive, p.object(key))
		if err == nil {
			err = publishCache(p.workspace, stage, entry.Paths)
		}
		if cleanupErr := removeCache(stage); cleanupErr != nil {
			logrus.WithError(cleanupErr).Warn("cache staging cleanup failed")
		}
		if err == nil {
			match := "exact-hit"
			if i > 0 {
				match = "fallback-hit"
			}
			logrus.WithFields(logrus.Fields{"key": key, "result": match}).Info("cache restored")
			return
		}
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound") {
			logrus.WithFields(logrus.Fields{"key": key, "result": "miss"}).Info("cache object missing")
			continue
		}
		logrus.WithError(err).WithField("key", key).Warn("cache restore failed; continuing cold")
		return
	}
	logrus.WithField("key", entry.resolved).Info("all cache candidates missing; continuing cold")
}
