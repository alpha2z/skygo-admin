// Package control exposes the versioned signed management contract.
package control

import core "github.com/alpha2z/skygo-admin/internal/control"

type Archive = core.Archive
type Progress = core.Progress
type Cleanup = core.Cleanup
type Service = core.Service
type Command = core.Command
type Envelope = core.Envelope
type Result = core.Result
type Observation = core.Observation
type Heartbeat = core.Heartbeat
type UnitPlan = core.UnitPlan
type UnitTarget = core.UnitTarget
type UnitProof = core.UnitProof

const Version = core.Version

var Sign = core.Sign
var Verify = core.Verify
var Digest = core.Digest

func ValidIdentifier(value string) bool { return core.Identifier.MatchString(value) }
func ImageIdentity(value string) bool   { return core.ImageIdentity(value) }
