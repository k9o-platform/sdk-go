package kibo

import "k9o.dev/sdk-go/kibo/clientcore"

// The client core for Crypto on a BYOK or HYOK project lives in the pure-Go
// package k9o.dev/sdk-go/kibo/clientcore; these names re-export it.
type (
	ClientCore       = clientcore.ClientCore
	ClientCoreConfig = clientcore.Config
	AppCore          = clientcore.AppCore
)

// NewClientCore builds the client core. Register it with client.Crypto.UseClientCore.
var NewClientCore = clientcore.New
