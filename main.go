// Copyright (c) 2020, the Drone Plugins project authors.
// Please see the AUTHORS file for details. All rights reserved.
// Use of this source code is governed by an Apache 2.0 license that can be
// found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/drone-plugins/drone-s3-cache/plugin"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
)

var version = "unknown"

func main() {
	if err := newApp().Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newApp() *cli.App {
	s := &plugin.Settings{}
	return &cli.App{
		Name: "drone-s3-cache", Usage: "缓存工作区目录到 S3 兼容存储", Version: version,
		Flags: settingsFlags(s),
		Action: func(ctx *cli.Context) error {
			level, err := logrus.ParseLevel(ctx.String("log-level"))
			if err != nil {
				return fmt.Errorf("log-level: %w", err)
			}
			logrus.SetLevel(level)
			s.FlushPathSet, s.FlushAgeSet = ctx.IsSet("flush-path"), ctx.IsSet("flush-age")
			s.CacheSet = ctx.IsSet("cache")
			p := plugin.New(*s)
			if err := p.Validate(); err != nil {
				return fmt.Errorf("configuration: %w", err)
			}
			return p.Execute()
		},
	}
}

func settingsFlags(s *plugin.Settings) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "mode", Usage: "操作：restore（只恢复）、rebuild（只回存）、flush（删除过期对象）；必填", EnvVars: []string{"PLUGIN_MODE"}, Destination: &s.Mode},
		&cli.StringFlag{Name: "cache", Usage: "完整 JSON 缓存对象或对象数组；restore/rebuild 必填，[] 跳过；key.files 支持 *、**，paths 为明确目录", EnvVars: []string{"PLUGIN_CACHE"}, Destination: &s.Cache},
		&cli.BoolFlag{Name: "key-from-git", Usage: "按固定提交的 Tree/Blob 计算文件 key；false 时读取工作区，不影响归档目录", Value: true, EnvVars: []string{"PLUGIN_KEY_FROM_GIT"}, Destination: &s.KeyFromGit},
		&cli.StringFlag{Name: "commit-sha", Usage: "Git 模式的完整提交 SHA；不自动用 HEAD 替代；仅文件 key 需要", EnvVars: []string{"PLUGIN_COMMIT_SHA", "DRONE_COMMIT_SHA"}, Destination: &s.CommitSHA},
		&cli.StringFlag{Name: "bucket", Usage: "已存在的 S3 桶名，不能含对象路径；非空操作必填", EnvVars: []string{"PLUGIN_BUCKET"}, Destination: &s.Bucket},
		&cli.StringFlag{Name: "flush-path", Usage: "清理前缀，默认 <owner>/<repo>/；只允许缩小到该仓库内，仅用于 flush", EnvVars: []string{"PLUGIN_FLUSH_PATH"}, Destination: &s.FlushPath},
		&cli.IntFlag{Name: "flush-age", Usage: "删除最后写入时间早于指定天数的对象；必须为正整数，仅用于 flush", Value: 30, EnvVars: []string{"PLUGIN_FLUSH_AGE"}, Destination: &s.FlushAge},
		&cli.StringFlag{Name: "endpoint", Usage: "HTTP/HTTPS S3 服务入口，不携带桶名、路径或区域推断", Value: "https://s3.amazonaws.com", EnvVars: []string{"PLUGIN_ENDPOINT", "CACHE_S3_ENDPOINT"}, Destination: &s.S3Options.Endpoint},
		&cli.StringFlag{Name: "region", Usage: "S3 区域；为空时由 SDK 配置获取，最终默认 us-east-1", EnvVars: []string{"PLUGIN_REGION", "CACHE_S3_REGION"}, Destination: &s.S3Options.Region},
		&cli.StringFlag{Name: "access-key", Usage: "S3 access key；与 secret-key 成对，与凭据文件互斥", EnvVars: []string{"PLUGIN_ACCESS_KEY", "CACHE_S3_ACCESS_KEY", "AWS_ACCESS_KEY_ID"}, Destination: &s.S3Options.Access},
		&cli.StringFlag{Name: "secret-key", Usage: "S3 secret key；与 access-key 成对", EnvVars: []string{"PLUGIN_SECRET_KEY", "CACHE_S3_SECRET_KEY", "AWS_SECRET_ACCESS_KEY"}, Destination: &s.S3Options.Secret},
		&cli.StringFlag{Name: "session-token", Usage: "临时凭据 token，与对应 access/secret 配合", EnvVars: []string{"PLUGIN_SESSION_TOKEN", "CACHE_S3_SESSION_TOKEN", "AWS_SESSION_TOKEN"}, Destination: &s.S3Options.Token},
		&cli.StringFlag{Name: "file-credentials", Usage: "容器内共享凭据文件；文件须存在，不与显式 access/secret 混用", EnvVars: []string{"PLUGIN_FILE_CREDENTIALS", "AWS_SHARED_CREDENTIALS_FILE"}, Destination: &s.S3Options.FileCredentials},
		&cli.StringFlag{Name: "profile", Usage: "SDK 命名配置；为空时沿用 SDK 默认选择", EnvVars: []string{"PLUGIN_PROFILE", "AWS_PROFILE"}, Destination: &s.S3Options.Profile},
		&cli.StringFlag{Name: "addressing-style", Usage: "桶寻址方式：auto、path、virtual；自定义入口 auto 使用 path", Value: "auto", EnvVars: []string{"PLUGIN_ADDRESSING_STYLE", "CACHE_S3_ADDRESSING_STYLE"}, Destination: &s.S3Options.AddressingStyle},
		&cli.DurationFlag{Name: "timeout", Usage: "单次 S3 操作超时，例如 30s、5m；必须大于 0", Value: 10 * time.Minute, EnvVars: []string{"PLUGIN_TIMEOUT", "CACHE_S3_TIMEOUT"}, Destination: &s.S3Options.Timeout},
		&cli.StringFlag{Name: "accelerated-endpoint", Usage: "AWS 专用加速入口主机名，不带 scheme；不用于通用 S3 服务", EnvVars: []string{"PLUGIN_ACCELERATED_ENDPOINT", "CACHE_S3_ACCELERATED_ENDPOINT"}, Destination: &s.S3Options.AcceleratedEndpoint},
		&cli.StringFlag{Name: "repo.owner", Usage: "仓库所有者或命名空间；非空操作必填", EnvVars: []string{"DRONE_REPO_OWNER", "DRONE_REPO_NAMESPACE"}, Destination: &s.Owner},
		&cli.StringFlag{Name: "repo.name", Usage: "仓库名称；非空操作必填", EnvVars: []string{"DRONE_REPO_NAME"}, Destination: &s.Repo},
		&cli.StringFlag{Name: "log-level", Usage: "日志级别：panic、fatal、error、warn、warning、info、debug、trace；未知值报错", Value: "info", EnvVars: []string{"PLUGIN_LOG_LEVEL"}},
	}
}
