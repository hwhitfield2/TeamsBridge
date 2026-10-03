package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"
	"teamsbridge.local/teamsbridge/pkg/connector"
)

func main() {
	// Configs, logs and SQLite contain work messages and delegated credentials.
	// Shell launchers also set umask 077; the working directory must be private.
	m := mxmain.BridgeMain{Name: "teamsbridge", Description: "Microsoft Teams for Beeper using Microsoft Graph", Version: "0.1.0", Connector: &connector.Connector{}}
	m.InitVersion("0.1.0", "local", "")
	m.Run()
}
