package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/drone-plugins/drone-s3-cache/plugin"
	"github.com/urfave/cli/v2"
)

func TestSettingsFromEnvironment(t *testing.T) {
	for k, v := range map[string]string{
		"PLUGIN_KEY": "deps-v1", "PLUGIN_KEY_FILES": "go.mod,go.sum", "PLUGIN_FALLBACK_KEYS": "branch,main",
		"PLUGIN_ADDRESSING_STYLE": "virtual", "PLUGIN_TIMEOUT": "45s",
		"PLUGIN_ENDPOINT": "https://obs.example", "PLUGIN_ACCESS_KEY": "access", "PLUGIN_SECRET_KEY": "secret",
	} {
		t.Setenv(k, v)
	}
	settings := &plugin.Settings{}
	app := &cli.App{Flags: settingsFlags(settings), Action: func(*cli.Context) error { return nil }}
	if err := app.Run([]string{"cache"}); err != nil {
		t.Fatal(err)
	}
	if settings.Key != "deps-v1" || !reflect.DeepEqual(settings.KeyFiles.Value(), []string{"go.mod", "go.sum"}) || !reflect.DeepEqual(settings.FallbackKeys.Value(), []string{"branch", "main"}) {
		t.Fatalf("new key settings not loaded: %#v", settings)
	}
	if settings.S3Options.AddressingStyle != "virtual" || settings.S3Options.Timeout != 45*time.Second {
		t.Fatal("S3 options not loaded")
	}
}
