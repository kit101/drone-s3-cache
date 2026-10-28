package plugin

import (
	"errors"

	"github.com/drone-plugins/drone-s3-cache/storage/s3"
	"github.com/drone/drone-cache-lib/archive"
	"github.com/sirupsen/logrus"
)

func (p *Plugin) restoreByKey(s cacheStorage, a archive.Archive) error {
	keys := append([]string{p.settings.resolvedKey}, p.settings.FallbackKeys.Value()...)
	seen := make(map[string]bool)
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		err := restoreCache(s, a, p.keyPath(key))
		fields := logrus.Fields{"resolved_key": p.settings.resolvedKey, "matched_key": key}
		if err == nil {
			result := "exact_hit"
			if key != p.settings.resolvedKey {
				result = "fallback_hit"
			}
			fields["cache_result"] = result
			logrus.WithFields(fields).Info("cache restored")
			return nil
		}
		if !errors.Is(err, s3.ErrCacheMiss) {
			fields["cache_result"] = "error"
			logrus.WithFields(fields).WithError(err).Error("cache restore failed")
			return err
		}
	}
	logrus.WithFields(logrus.Fields{"resolved_key": p.settings.resolvedKey, "cache_result": "miss"}).Info("cache not found; continuing without cache")
	return nil
}
