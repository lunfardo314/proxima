package node

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
	"golang.org/x/net/context"
)

func (p *ProximaNode) startApiServer() {
	port := viper.GetInt("api.server.port")
	p.echoServer = echo.New()
	p.echoServer.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "Hello, World!")
	})
	p.log.Infof("starting API server on port %d", port)
	go func() {
		err := p.echoServer.Start(fmt.Sprintf(":%d", port))
		util.Assertf(err == nil || errors.Is(err, http.ErrServerClosed), "err == nil || errors.Is(err, http.ErrServerClosed)")
	}()
}

func (p *ProximaNode) stopApiServer() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := p.echoServer.Shutdown(ctx)
	util.AssertNoError(err)

	p.log.Infof("API server has been stopped")
}
