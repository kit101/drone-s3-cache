// Copyright (c) 2020, the Drone Plugins project authors.
// Please see the AUTHORS file for details. All rights reserved.
// Use of this source code is governed by an Apache 2.0 license that can be
// found in the LICENSE file.

package plugin

import "github.com/drone-plugins/drone-s3-cache/storage/s3"

type Settings struct {
	Mode, Cache, Bucket, Owner, Repo string
	KeyFromGit                       bool
	CommitSHA                        string
	FlushPath                        string
	FlushAge                         int
	FlushPathSet, FlushAgeSet        bool
	CacheSet                         bool
	S3Options                        s3.Options
}

type Plugin struct {
	settings  Settings
	entries   []cacheEntry
	workspace string
}

func New(settings Settings) *Plugin { return &Plugin{settings: settings} }
