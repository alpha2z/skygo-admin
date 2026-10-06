package app

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"github.com/alpha2z/skygo-admin/internal/control"
	"github.com/alpha2z/skygo-admin/internal/settings"
	"gorm.io/gorm"
	"io/fs"
	"os"
	"strings"
	"time"
)

type Config struct {
	PluginInventories                                  map[string][]string
	IndependentApprovalEnabled                         bool
	Workflows                                          map[string]WorkflowProvider
	TaskPolicy                                         func(context.Context, *gorm.DB, control.Command) error
	RequestTimeout                                     time.Duration
	UnixSocket                                         string
	ReleaseRepository                                  string
	ReleaseWorkflow                                    string
	WebFS                                              fs.FS
	AgentActions                                       map[string]AgentAction
	Extensions                                         []Extension
	DistributionConfig                                 string
	ListenAddress, MySQLDSN, JWTSecret, BootstrapToken string
	CookieSecure, TOTPEnabled, SkipEmailConfirmation   bool
	TrustedProxyCIDRs                                  []string
	SigningKey                                         ed25519.PrivateKey
	SMTPAddress, SMTPUsername, SMTPPassword, SMTPFrom  string
	GitHubConfig                                       string
	WebRoot                                            string
}

func LoadConfig() (Config, error) {
	c := Config{UnixSocket: os.Getenv("ADMIN_UNIX_SOCKET"), ListenAddress: "127.0.0.1:18391", CookieSecure: true, TOTPEnabled: true, WebRoot: "admin-web"}
	if v := os.Getenv("ADMIN_LISTEN_ADDRESS"); v != "" {
		c.ListenAddress = v
	}
	if v := os.Getenv("ADMIN_WEB_ROOT"); v != "" {
		c.WebRoot = v
	}
	for name, dest := range map[string]*string{"ADMIN_MYSQL_DSN_FILE": &c.MySQLDSN, "ADMIN_JWT_SECRET_FILE": &c.JWTSecret, "ADMIN_BOOTSTRAP_TOKEN_FILE": &c.BootstrapToken} {
		v, err := settings.Secret(os.Getenv(name))
		if err != nil {
			return c, errors.New(name + " is required and must be private")
		}
		*dest = v
	}
	raw, err := settings.Secret(os.Getenv("ADMIN_SIGNING_KEY_FILE"))
	if err != nil {
		return c, err
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return c, errors.New("invalid signing key file")
	}
	c.SigningKey = key
	if len(c.JWTSecret) < 32 || len(c.BootstrapToken) < 32 {
		return c, errors.New("admin secrets must contain at least 32 bytes")
	}
	for name, dest := range map[string]*bool{"ADMIN_COOKIE_SECURE": &c.CookieSecure, "ADMIN_TOTP_ENABLED": &c.TOTPEnabled} {
		switch os.Getenv(name) {
		case "", "true":
		case "false":
			*dest = false
		default:
			return c, errors.New("invalid boolean setting")
		}
	}
	switch os.Getenv("ADMIN_EMAIL_CONFIRMATION_ENABLED") {
	case "", "true":
	case "false":
		c.SkipEmailConfirmation = true
	default:
		return c, errors.New("invalid email confirmation setting")
	}
	switch os.Getenv("ADMIN_INDEPENDENT_APPROVAL_ENABLED") {
	case "", "false":
	case "true":
		c.IndependentApprovalEnabled = true
	default:
		return c, errors.New("invalid ADMIN_INDEPENDENT_APPROVAL_ENABLED")
	}
	c.SMTPAddress = os.Getenv("ADMIN_SMTP_ADDRESS")
	c.SMTPUsername = os.Getenv("ADMIN_SMTP_USERNAME")
	c.SMTPFrom = os.Getenv("ADMIN_SMTP_FROM")
	if p := os.Getenv("ADMIN_SMTP_PASSWORD_FILE"); p != "" {
		c.SMTPPassword, err = settings.Secret(p)
		if err != nil {
			return c, err
		}
	}
	if !c.SkipEmailConfirmation && (c.SMTPAddress == "" || c.SMTPFrom == "") {
		return c, errors.New("configure SMTP or explicitly disable email confirmation")
	}
	if v := os.Getenv("ADMIN_TRUSTED_PROXY_CIDRS"); v != "" {
		c.TrustedProxyCIDRs = strings.Split(v, ",")
	}
	c.DistributionConfig = os.Getenv("ADMIN_DISTRIBUTION_CONFIG")
	c.GitHubConfig = os.Getenv("ADMIN_GITHUB_CONFIG")
	if err := loadPluginActions(&c); err != nil {
		return c, err
	}
	return c, nil
}
