package agent

import (
	"encoding/json"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"os"
	"path/filepath"
	"syscall"
)

// ResolveFailed closes an uncertain receipt after an operator has inspected the
// host. It never runs a command or asserts success. Stop the agent first.
func (a *Agent) ResolveFailed(id string) error {
	if !control.Identifier.MatchString(id) {
		return errors.New("invalid receipt ID")
	}
	f, err := os.OpenFile(filepath.Join(a.cfg.StateDir, "agent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return errors.New("journal unavailable")
	}
	defer f.Close()
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return errors.New("stop the agent before operator resolution")
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if _, err := os.Stat(filepath.Join(a.cfg.StateDir, id+".unit.json")); !os.IsNotExist(err) {
		return errors.New("recovery units require verified reconciliation of both members")
	}
	path := filepath.Join(a.cfg.StateDir, id+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return errors.New("receipt unavailable")
	}
	var r receipt
	if json.Unmarshal(b, &r) != nil || r.Command.ID != id || r.Command.HostID != a.cfg.HostID || r.Result.Status != "uncertain" {
		return errors.New("receipt is not an unresolved local operation")
	}
	r.Result = control.Result{ID: id, Status: "failed", Code: "OPERATOR_RESOLVED_FAILED"}
	return a.save(path, r)
}
