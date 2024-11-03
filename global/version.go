package global

import (
	"fmt"
)

// Version is the version of the Proxima node
const (
	Version        = "v0.1.3-testnet"
	bannerTemplate = "starting Proxima node version %s"
)

func BannerString() string {
	return fmt.Sprintf(bannerTemplate, Version)
}
