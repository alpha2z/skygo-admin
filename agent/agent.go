// Package agent provides the signed execution core and local extension contract.
package agent

import "github.com/alpha2z/skygo-admin/internal/agent"

type Config = agent.Config
type LocalService = agent.LocalService
type LocalUnit = agent.LocalUnit
type Agent = agent.Agent
type Driver = agent.Driver
type Docker = agent.Docker

var New = agent.New

type ExtensionAction = agent.ExtensionAction
