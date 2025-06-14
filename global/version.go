package global

import (
	"fmt"
	"runtime/debug"
)

const (
	// Version has the following structure: vA.B.C[-<label>]
	// A is the major version. It is 0 until beta. All alpha testnets are 'v0.n...'. Beta starts at 1
	// B is the minor version. Change of the version means breaking change
	// C is the subversion. Change of it means non-breaking change
	// <label> is an arbitrary label
	Version        = "v0.5.1-testnet"
	bannerTemplate = `
___  ____ ____ _  _ _ _  _ ____ 
|__] |__/ |  |  \/  | |\/| |__| 
|    |  \ |__| _/\_ | |  | |  | 
node version %s, commit hash: %s, commit time: %s 
`
	bannerTemplate2 = `
╔═╗┬─┐┌─┐─┐ ┬┬┌┬┐┌─┐
╠═╝├┬┘│ │┌┴┬┘││││├─┤
╩  ┴└─└─┘┴ └─┴┴ ┴┴ ┴
Proxima node version %s, commit hash: %s, commit time: %s
`
	bannerTemplate1 = `
   _____               _                 
 |  __ \             (_)                
 | |__) | __ _____  ___ _ __ ___   __ _ 
 |  ___/ '__/ _ \ \/ / | '_ ' _ \ / _' |
 | |   | | | (_) >  <| | | | | | | (_| |
 |_|   |_|  \___/_/\_\_|_| |_| |_|\__,_|
 version %s, commit hash: %s, commit time: %s
`
	bannerTemplate0 = "starting Proxima node version %s, commit hash: %s, commit time: %s"
)

var (
	CommitHash = "N/A"
	CommitTime = "N/A"
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
