package global

import (
	"fmt"
	"runtime/debug"
	"time"
)

const (
	// Version has the following structure: vA.B.C[-<label>]
	// A is the major version. It is 0 until beta. All alpha testnets are 'v0.n...'. Beta starts at 1
	// B is the minor version. Change of the version means breaking change
	// C is the subversion. Change of it means non-breaking change
	// <label> is an arbitrary label
	Version        = "v0.8.0-develop"
	bannerTemplate = `
___  ____ ____ _  _ _ _  _ ____ 
|__] |__/ |  |  \/  | |\/| |__| 
|    |  \ |__| _/\_ | |  | |  | 
node version %s, commit hash: %s, commit time: %s 
`
)

var (
	CommitHash       = "N/A"
	CommitTime       = "N/A"
	CommitTimeParsed time.Time
)

func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				CommitHash = setting.Value
			}
			if setting.Key == "vcs.time" {
				CommitTime = setting.Value
				CommitTimeParsed, _ = time.Parse(time.RFC3339, CommitTime)
			}
		}
	}
}

func BannerString() string {
	return fmt.Sprintf(bannerTemplate, Version, CommitHash, CommitTime)
}
