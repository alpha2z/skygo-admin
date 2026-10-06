package admin

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/alpha2z/skygo-admin/internal/app"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"golang.org/x/term"
	"gorm.io/gorm"
)

// RunPasswordChange changes an existing administrator's password using local
// database authority. Only ADMIN_MYSQL_DSN_FILE is required. Applications can
// supply an additional database guard before any account mutation.
func RunPasswordChange(username, passwordFile string, checkDatabase func(*gorm.DB) error) error {
	dsn, err := settings.Secret(os.Getenv("ADMIN_MYSQL_DSN_FILE"))
	if err != nil {
		return errors.New("ADMIN_MYSQL_DSN_FILE is required and must be private")
	}
	db, err := app.OpenDB(dsn)
	if err != nil {
		return err
	}
	sql, err := db.DB()
	if err != nil {
		return errors.New("management database unavailable")
	}
	defer sql.Close()
	if checkDatabase != nil {
		if err = checkDatabase(db); err != nil {
			return err
		}
	}
	password, err := readNewPassword(passwordFile, os.Stdin, os.Stderr)
	if err != nil {
		return err
	}
	defer clear(password)
	return app.ChangeAdminPassword(db, username, string(password))
}

func readNewPassword(path string, input *os.File, output io.Writer) ([]byte, error) {
	if path != "" {
		return readPasswordFile(path)
	}
	if !term.IsTerminal(int(input.Fd())) {
		return nil, errors.New("interactive terminal required; use -password-file for non-interactive execution")
	}
	read := func(prompt string) ([]byte, error) {
		fmt.Fprint(output, prompt)
		value, err := term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(output)
		if err != nil {
			clear(value)
			return nil, errors.New("password input cancelled or unavailable")
		}
		return value, nil
	}
	return confirmPassword(read)
}

func confirmPassword(read func(string) ([]byte, error)) ([]byte, error) {
	first, err := read("New password: ")
	if err != nil {
		return nil, err
	}
	second, err := read("Confirm new password: ")
	defer clear(second)
	if err != nil {
		clear(first)
		return nil, err
	}
	if !bytes.Equal(first, second) {
		clear(first)
		return nil, errors.New("passwords do not match; no password changed")
	}
	if err = app.ValidateAdminPassword(string(first)); err != nil {
		clear(first)
		return nil, err
	}
	return first, nil
}

func readPasswordFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("password file unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1026 {
		return nil, errors.New("password file must be regular, private (0600 or stricter), and at most 1026 bytes")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1027))
	if err != nil || len(raw) > 1026 {
		clear(raw)
		return nil, errors.New("password file unavailable or too large")
	}
	// Strip one conventional line ending, preserving spaces in the actual password.
	password := raw
	if bytes.HasSuffix(password, []byte("\n")) {
		password = bytes.TrimSuffix(bytes.TrimSuffix(password, []byte("\n")), []byte("\r"))
	}
	if err = app.ValidateAdminPassword(string(password)); err != nil {
		clear(raw)
		return nil, err
	}
	return password, nil
}
