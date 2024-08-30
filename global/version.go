package global

import (
	"fmt"
)

// Version is the version of the Proxima node
const Version = "0.1-alpha"

//const bannerTemplate = `
//---------------------------------------------------
//          Proxima node version %s
//---------------------------------------------------`

const bannerTemplate = "starting Proxima node version %s"

func BannerString() string {
	return fmt.Sprintf(bannerTemplate, Version)
}
