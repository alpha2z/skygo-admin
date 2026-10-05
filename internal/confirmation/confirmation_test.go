package confirmation

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type captureSender struct {
	code  string
	codes map[string]string
	fail  bool
}

func (s *captureSender) Send(_ context.Context, email, _, body string) error {
	if s.fail {
		return errors.New("test delivery failure")
	}
	s.code = strings.TrimPrefix(regexp.MustCompile(`confirmation code: [0-9]{8}`).FindString(strings.ToLower(body)), "confirmation code: ")
	if s.codes == nil {
		s.codes = map[string]string{}
	}
	s.codes[email] = s.code
	return nil
}
func TestBindingDigest(t *testing.T) {
	base := Digest("POST", "/api/ops/tasks", []byte(`{"environment":"one"}`))
	for _, v := range []string{Digest("POST", "/api/ops/tasks", []byte(`{"environment":"two"}`)), Digest("GET", "/api/ops/tasks", []byte(`{"environment":"one"}`)), Digest("POST", "/api/ops/tasks/open", []byte(`{"environment":"one"}`))} {
		if v == base {
			t.Fatal("unbound request")
		}
	}
	for _, email := range []string{"a@example.com\r\nBcc: victim@example.com", "display <a@example.com>", "bad"} {
		if ValidEmail(email) {
			t.Fatal("invalid email accepted")
		}
	}
}
func TestConfirmationMySQL(t *testing.T) {
	dsn := os.Getenv("SKYGO_ADMIN_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated MySQL required")
	}
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(cfg.DBName, "skygo_admin_test_") {
		t.Fatal("isolated database required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	name := cfg.DBName + "_confirmation"
	if err = db.Exec("CREATE DATABASE `" + name + "`").Error; err != nil {
		t.Fatal(err)
	}
	root := db
	defer root.Exec("DROP DATABASE `" + name + "`")
	cfg.DBName = name
	db, err = gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&Challenge{}, &Rate{}); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DELETE FROM admin_confirmation")
	defer db.Exec("DELETE FROM admin_confirmation_rate")
	sender := &captureSender{}
	s := Service{DB: db, Secret: []byte(strings.Repeat("s", 32)), Sender: sender}
	ctx := context.Background()
	issue := func(admin uint32) Challenge {
		t.Helper()
		row, err := s.Issue(ctx, admin, "operator@example.com", "127.0.0.1", "digest", "test")
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	row := issue(9901)
	code := sender.code
	if len(code) != 8 || strings.Contains(row.CodeHash, code) {
		t.Fatal("invalid code handling")
	}
	if _, err = s.Issue(ctx, 9901, "operator@example.com", "127.0.0.1", "digest", "test"); !errors.Is(err, ErrRate) {
		t.Fatalf("no send throttle: %v", err)
	}
	if _, _, err = s.Begin(ctx, 9902, row.ID, code, "digest"); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-admin accepted", err)
	}
	if _, _, err = s.Begin(ctx, 9901, row.ID, code, "changed"); !errors.Is(err, ErrInvalid) {
		t.Fatal("changed operation accepted", err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, replay, e := s.Begin(ctx, 9901, row.ID, code, "digest")
			if e == nil && !replay {
				won.Add(1)
			} else if !errors.Is(e, ErrPending) {
				t.Errorf("unexpected concurrent result %v", e)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal("duplicate execution", won.Load())
	}
	if err = s.Finish(ctx, row.ID, 202, `{"task_id":"one"}`); err != nil {
		t.Fatal(err)
	}
	again, replay, err := s.Begin(ctx, 9901, row.ID, code, "digest")
	if err != nil || !replay || again.Response != `{"task_id":"one"}` {
		t.Fatal("lost original result", again, replay, err)
	}
	locked := issue(9903)
	good := sender.code
	wrong := "00000000"
	if good == wrong {
		wrong = "11111111"
	}
	for i := 0; i < 5; i++ {
		if _, _, e := s.Begin(ctx, 9903, locked.ID, wrong, "digest"); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	if _, _, err = s.Begin(ctx, 9903, locked.ID, good, "digest"); !errors.Is(err, ErrInvalid) {
		t.Fatal("attempt limit bypass", err)
	}
	expired := issue(9904)
	db.Model(&expired).Update("expires_at", time.Now().Add(-time.Minute))
	if _, _, err = s.Begin(ctx, 9904, expired.ID, sender.code, "digest"); !errors.Is(err, ErrInvalid) {
		t.Fatal("expired accepted", err)
	}
	old := issue(9905)
	oldCode := sender.code
	db.Model(&Rate{}).Where("`key` = ?", "retry:"+s.hash("9905:digest")).Update("last_at", time.Now().Add(-2*time.Minute))
	issue(9905)
	if _, _, err = s.Begin(ctx, 9905, old.ID, oldCode, "digest"); !errors.Is(err, ErrInvalid) {
		t.Fatal("resend left old code usable", err)
	}
	// Different operation identities do not impose the resend cooldown on each other.
	if _, err = s.Issue(ctx, 9905, "operator@example.com", "127.0.0.1", "another-operation", "test"); err != nil {
		t.Fatal("unrelated operation forced to wait", err)
	}
	db.Model(&Rate{}).Where("`key` = ?", "admin:9905").Update("count", 60)
	if _, err = s.Issue(ctx, 9905, "operator@example.com", "127.0.0.1", "hourly-limit", "test"); !errors.Is(err, ErrRate) {
		t.Fatal("account hourly limit bypass", err)
	}
	// Rebinding requires independently delivered codes from both addresses.
	paired, err := s.Issue(ctx, 9910, "new@example.com", "127.0.0.1", "binding", "rebind", "old@example.com")
	if err != nil {
		t.Fatal(err)
	}
	newCode, oldCode := sender.codes["new@example.com"], sender.codes["old@example.com"]
	applied := 0
	apply := func(*gorm.DB, Challenge) error { applied++; return nil }
	if _, _, err = s.Apply(ctx, 9910, paired.ID, newCode, "binding", apply); !errors.Is(err, ErrInvalid) {
		t.Fatal("rebind accepted without old mailbox", err)
	}
	if applied != 0 {
		t.Fatal("invalid rebind applied")
	}
	if _, _, err = s.Apply(ctx, 9910, paired.ID, newCode+":"+oldCode, "binding", apply); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := s.Apply(ctx, 9910, paired.ID, newCode+":"+oldCode, "binding", apply); err != nil || !replay || applied != 1 {
		t.Fatal("rebind replay changed profile", replay, err, applied)
	}
	sender.fail = true
	if _, err = s.Issue(ctx, 9906, "operator@example.com", "127.0.0.1", "digest", "test"); err == nil {
		t.Fatal("send failure hidden")
	}
	var failed Challenge
	db.Where("admin_id = ?", 9906).First(&failed)
	if failed.Status != "send_failed" {
		t.Fatal("failed delivery usable")
	}
}

func TestSMTPRequiresTLSAndDeliversToLoopbackFixture(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		fmt.Fprint(conn, "220 fixture ESMTP\r\n")
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				fmt.Fprint(conn, "250 fixture\r\n")
			case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
				fmt.Fprint(conn, "250 OK\r\n")
			case strings.HasPrefix(line, "DATA"):
				fmt.Fprint(conn, "354 continue\r\n")
				var body strings.Builder
				for {
					line, e = reader.ReadString('\n')
					if e != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
					body.WriteString(line)
				}
				received <- body.String()
				fmt.Fprint(conn, "250 queued\r\n")
			case strings.HasPrefix(line, "QUIT"):
				fmt.Fprint(conn, "221 bye\r\n")
				return
			default:
				fmt.Fprint(conn, "500 unsupported\r\n")
			}
		}
	}()
	sender := SMTP{Address: listener.Addr().String(), From: "admin@example.com", AllowLoopbackPlaintext: true}
	if err = sender.Send(context.Background(), "operator@example.com", "test", "Confirmation code: 12345678"); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-received:
		if !strings.Contains(body, "12345678") || !strings.Contains(body, "To: operator@example.com") {
			t.Fatal("mail content missing")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
	<-done
	if err = (SMTP{Address: "127.0.0.1:25", From: "invalid\r\nFrom:bad"}).Send(context.Background(), "operator@example.com", "test", "body"); err == nil {
		t.Fatal("header injection accepted")
	}
}

func TestResponseEncryptionBindsReceiptIdentity(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	body := `{"node_secret":"fixture-private-value"}`
	sealed, err := SealResponse(secret, "receipt-one", body)
	if err != nil || strings.Contains(sealed, "fixture-private-value") {
		t.Fatal("plaintext secret persisted", err)
	}
	if plain, err := openResponse(secret, "receipt-one", sealed); err != nil || plain != body {
		t.Fatal("cannot recover encrypted response", err)
	}
	if _, err = openResponse(secret, "receipt-two", sealed); err == nil {
		t.Fatal("ciphertext moved to another receipt")
	}
	if _, err = openResponse([]byte(strings.Repeat("t", 32)), "receipt-one", sealed); err == nil {
		t.Fatal("wrong secret accepted")
	}
}
