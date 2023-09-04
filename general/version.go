package general

import (
	"fmt"
)

// Version is the version of the Proxima node
const Version = "0.0"

const bannerTemplate = `
---------------------------------------------------
          Proxima node version %s
---------------------------------------------------
`

func BannerString() string {
	return fmt.Sprintf(bannerTemplate, Version)
}
