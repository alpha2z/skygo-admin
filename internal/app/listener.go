package app

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"
)

func (s *Server) listen() (net.Listener, error) {
	if s.cfg.UnixSocket == "" {
		return net.Listen("tcp", s.cfg.ListenAddress)
	}
	file := s.cfg.UnixSocket
	if !filepath.IsAbs(file) {
		return nil, errors.New("absolute socket path required")
	}
	if err := os.MkdirAll(filepath.Dir(file), 0750); err != nil {
		return nil, errors.New("socket directory unavailable")
	}
	if info, err := os.Lstat(file); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("socket path already contains a file")
		}
		if connection, err := net.DialTimeout("unix", file, time.Second); err == nil {
			connection.Close()
			return nil, errors.New("socket already in use")
		}
		if os.Remove(file) != nil {
			return nil, errors.New("stale socket unavailable")
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.New("socket path unavailable")
	}
	listener, err := net.Listen("unix", file)
	if err != nil {
		return nil, errors.New("socket listener unavailable")
	}
	if os.Chmod(file, 0660) != nil {
		listener.Close()
		return nil, errors.New("socket permissions unavailable")
	}
	return listener, nil
}
