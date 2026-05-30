package main

import (
	_ "github.com/beeper/ai-bridge/pkg/ai/providers"
	"github.com/beeper/ai-bridge/pkg/connector"
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"
)

var (
	Tag       = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

var aiConnector = &connector.Connector{}

var m = mxmain.BridgeMain{
	Name:        "ai",
	URL:         "https://github.com/beeper/ai-bridge",
	Description: "A Beeper AI bridge.",
	Version:     "0.1.0",
	Connector:   aiConnector,
}

func main() {
	m.PostInit = func() {
		aiConnector.AppServiceToken = m.Config.AppService.ASToken
		aiConnector.HomeserverURL = m.Config.Homeserver.Address
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
