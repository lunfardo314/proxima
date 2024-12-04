package global

import (
	"fmt"
	"runtime/debug"
)

// Version is the version of the Proxima node
const (
	Version        = "v0.1.3-testnet"
	bannerTemplate = "starting Proxima node version %s, commit hash: %s, commit time: %s"
)

var (
	CommitHash = "N/A"
	CommitTime = "N?A"
)

func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				CommitHash = setting.Value
			}
			if setting.Key == "vcs.time" {
				CommitTime = setting.Value
			}
		}
	}
}

func BannerString() string {
	return fmt.Sprintf(bannerTemplate, Version, CommitHash, CommitTime)
}
