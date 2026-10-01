// Package admin exposes the supported composition API for privately built extensions.
// Extension implementations remain outside this module.
package admin

import (
	"github.com/alpha2z/skygo-admin/internal/app"
	"github.com/alpha2z/skygo-admin/internal/settings"
)

type Config = app.Config
type Server = app.Server
type Extension = app.Extension
type Route = app.ExtensionRoute
type Page = app.ExtensionPage
type User = app.AdminUser
type RolePolicy = app.RolePolicy
type Task = app.Task

var LoadConfig = app.LoadConfig
var OpenDB = app.OpenDB
var Migrate = app.Migrate
var NewServer = app.NewServer

var ExtensionPolicies = app.ExtensionPolicies

type AgentAction = app.AgentAction

var ReadSecret = settings.Secret

type Host = app.Host
type ServiceRecord = app.ServiceRecord

type WorkflowProvider = app.WorkflowProvider
type WorkflowPlan = app.WorkflowPlan
type WorkflowTarget = app.WorkflowTarget
type WorkflowStep = app.WorkflowStep
type Workflow = app.Workflow
