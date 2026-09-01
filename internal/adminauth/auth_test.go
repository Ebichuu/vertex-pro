package adminauth

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

func TestInitializeRequiresStrongPasswordAndIsOneShot(t *testing.T) {
	database := openMigrated(t)
	service, err := New(database)
	if err != nil {
		t.Fatal(err)
	}
	initialized, err := service.Initialized(context.Background())
	if err != nil || initialized {
		t.Fatalf("initialized=%t err=%v", initialized, err)
	}
	if err := service.Initialize(context.Background(), "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password error = %v", err)
	}
	if err := service.Initialize(context.Background(), "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	if err := service.Initialize(context.Background(), "another-strong-passphrase"); !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("second initialize error = %v", err)
	}
	initialized, err = service.Initialized(context.Background())
	if err != nil || !initialized {
		t.Fatalf("initialized=%t err=%v", initialized, err)
	}
}

func TestLoginBeforeInitializeFails(t *testing.T) {
	database := openMigrated(t)
	service, _ := New(database)
	if _, err := service.Login(context.Background(), "correct-horse-battery", time.Now(), time.Hour); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoginLocksAfterRepeatedFailures(t *testing.T) {
	database := openMigrated(t)
	service, _ := New(database)
	if err := service.Initialize(context.Background(), "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 6, 0, 0, 0, time.UTC)
	ctx := context.Background()
	for attempt := 0; attempt < maxFailedAttempts; attempt++ {
		if _, err := service.Login(ctx, "wrong-password!!", now, time.Hour); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d error = %v", attempt, err)
		}
	}
	// 达到上限后即使口令正确也锁定。
	if _, err := service.Login(ctx, "correct-horse-battery", now.Add(time.Minute), time.Hour); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked error = %v", err)
	}
	// 锁定窗口内继续累计失败仍拒绝。
	if _, err := service.Login(ctx, "wrong-password!!", now.Add(2*time.Minute), time.Hour); !errors.Is(err, ErrLocked) {
		t.Fatalf("lockout window error = %v", err)
	}
	// 锁定过期后正确口令成功。
	session, err := service.Login(ctx, "correct-horse-battery", now.Add(lockoutDuration+time.Minute), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if session.Token == "" || !session.ExpiresAt.After(now) {
		t.Fatalf("session = %+v", session)
	}
}

func TestLoginIssuesSingleFixedSession(t *testing.T) {
	database := openMigrated(t)
	service, _ := New(database)
	if err := service.Initialize(context.Background(), "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 6, 0, 0, 0, time.UTC)
	first, err := service.Login(context.Background(), "correct-horse-battery", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Login(context.Background(), "correct-horse-battery", now.Add(time.Minute), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token {
		t.Fatal("new login must issue a fresh token")
	}
	valid, err := service.ValidateSession(context.Background(), second.Token, now.Add(2*time.Minute))
	if err != nil || !valid {
		t.Fatalf("second session valid=%t err=%v", valid, err)
	}
	valid, err = service.ValidateSession(context.Background(), first.Token, now.Add(2*time.Minute))
	if err != nil || valid {
		t.Fatalf("old session must be invalid: valid=%t err=%v", valid, err)
	}
	if _, err := service.ValidateSession(context.Background(), "forged-token", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestSessionExpiryAndLogout(t *testing.T) {
	database := openMigrated(t)
	service, _ := New(database)
	if err := service.Initialize(context.Background(), "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 6, 0, 0, 0, time.UTC)
	session, err := service.Login(context.Background(), "correct-horse-battery", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := service.ValidateSession(context.Background(), session.Token, now.Add(59*time.Minute))
	if err != nil || !valid {
		t.Fatalf("valid=%t err=%v", valid, err)
	}
	valid, err = service.ValidateSession(context.Background(), session.Token, now.Add(time.Hour))
	if err != nil || valid {
		t.Fatalf("expired session valid=%t err=%v", valid, err)
	}

	active, err := service.Login(context.Background(), "correct-horse-battery", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(context.Background(), active.Token); err != nil {
		t.Fatal(err)
	}
	valid, err = service.ValidateSession(context.Background(), active.Token, now.Add(time.Minute))
	if err != nil || valid {
		t.Fatalf("after logout valid=%t err=%v", valid, err)
	}
	// 用错误令牌登出为幂等操作，不影响状态。
	if err := service.Logout(context.Background(), "other-token"); err != nil {
		t.Fatal(err)
	}
}

func TestPasswordHashFormatIsPbkdf2(t *testing.T) {
	hash, err := hashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, hashAlgorithm+"$"+itoa(hashIterations)+"$") {
		t.Fatalf("hash = %s", hash)
	}
	if !verifyPassword(hash, "correct-horse-battery") {
		t.Fatal("correct password rejected")
	}
	if verifyPassword(hash, "wrong-password!!") {
		t.Fatal("wrong password accepted")
	}
	if verifyPassword("bogus$1$aa$bb", "correct-horse-battery") {
		t.Fatal("malformed hash accepted")
	}
}

func itoa(value int) string {
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "admin-auth.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}
