package adminauth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

var (
	ErrAlreadyInitialized = errors.New("admin credential already initialized")
	ErrNotInitialized     = errors.New("admin credential is not initialized")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrLocked             = errors.New("admin login temporarily locked")
	ErrWeakPassword       = errors.New("password does not satisfy the policy")
)

const (
	metaPasswordHash   = "admin.password_hash"
	metaFailedAttempts = "admin.failed_attempts"
	metaLockoutUntil   = "admin.lockout_until"
	metaSessionToken   = "admin.session_token_sha256"
	metaSessionExpires = "admin.session_expires_at"

	hashAlgorithm  = "pbkdf2-sha256"
	hashIterations = 600000
	hashKeyLength  = 32
	saltLength     = 16

	// 单管理员产品的暴力尝试防线：连续失败 5 次锁定 15 分钟（成功登录清零）。
	maxFailedAttempts = 5
	lockoutDuration   = 15 * time.Minute

	// 最小口令长度为 local-only 默认策略，修改需记录。
	minPasswordLength = 12

	sessionTokenLength = 32
)

// queryable 同时由 *sql.DB 与 *db.ImmediateTx 满足，使元数据读写既可
// 在事务内也可在事务外执行。
type queryable interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Session 是签发给管理员的唯一活动会话；原始令牌只在签发时返回一次。
type Session struct {
	Token     string
	ExpiresAt time.Time
}

// Service 管理单管理员口令与唯一活动会话。凭据与会话状态保存在
// vertex_pro_meta；会话令牌仅保存 SHA-256。
type Service struct {
	database *sql.DB
}

// New 校验数据库非空。
func New(database *sql.DB) (*Service, error) {
	if database == nil {
		return nil, errors.New("database is required")
	}
	return &Service{database: database}, nil
}

// Initialized 报告管理员凭据是否已完成首次初始化。
func (s *Service) Initialized(ctx context.Context) (bool, error) {
	value, err := getMeta(ctx, s.database, metaPasswordHash)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(value) != "", nil
}

// Initialize 首次设置管理员口令；重复初始化被拒绝，口令须满足最小长度。
func (s *Service) Initialize(ctx context.Context, password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return db.RunImmediate(ctx, s.database, func(ctx context.Context, tx *db.ImmediateTx) error {
		existing, err := getMeta(ctx, tx, metaPasswordHash)
		if err != nil {
			return err
		}
		if strings.TrimSpace(existing) != "" {
			return ErrAlreadyInitialized
		}
		return setMetas(ctx, tx, map[string]string{metaPasswordHash: hash})
	})
}

// Login 校验口令并签发唯一活动会话：新会话替换旧会话（固定会话语义）。
// 连续失败达到上限后锁定，锁定窗口内即使口令正确也拒绝。
func (s *Service) Login(ctx context.Context, password string, now time.Time, sessionTTL time.Duration) (Session, error) {
	if sessionTTL <= 0 {
		return Session{}, errors.New("session ttl must be positive")
	}
	var session Session
	// loginErr 在事务提交后返回：失败计数必须与拒绝决定一起持久化，
	// 否则计数会随拒绝事务一起回滚。
	var loginErr error
	err := db.RunImmediate(ctx, s.database, func(ctx context.Context, tx *db.ImmediateTx) error {
		hash, err := getMeta(ctx, tx, metaPasswordHash)
		if err != nil {
			return err
		}
		if strings.TrimSpace(hash) == "" {
			loginErr = ErrNotInitialized
			return nil
		}
		if err := enforceLockout(ctx, tx, now); err != nil {
			loginErr = err
			return nil
		}
		if !verifyPassword(hash, password) {
			if err := recordFailure(ctx, tx, now); err != nil {
				return err
			}
			loginErr = ErrInvalidCredentials
			return nil
		}
		return resetFailuresAndIssueSession(ctx, tx, now, sessionTTL, &session)
	})
	if err != nil {
		return Session{}, err
	}
	if loginErr != nil {
		return Session{}, loginErr
	}
	return session, nil
}

// ValidateSession 校验令牌是否对应唯一活动会话且未过期。
func (s *Service) ValidateSession(ctx context.Context, token string, now time.Time) (bool, error) {
	if strings.TrimSpace(token) == "" {
		return false, nil
	}
	stored, err := getMeta(ctx, s.database, metaSessionToken)
	if err != nil {
		return false, err
	}
	expires, err := getMeta(ctx, s.database, metaSessionExpires)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(stored) == "" || strings.TrimSpace(expires) == "" {
		return false, nil
	}
	expiresAt, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return false, fmt.Errorf("parse session expiry: %w", err)
	}
	if !now.UTC().Before(expiresAt) {
		return false, nil
	}
	return subtle.ConstantTimeCompare(sessionDigest(token), decodeDigest(stored)) == 1, nil
}

// Logout 注销当前唯一会话；令牌不匹配时保持现状（幂等）。
func (s *Service) Logout(ctx context.Context, token string) error {
	return db.RunImmediate(ctx, s.database, func(ctx context.Context, tx *db.ImmediateTx) error {
		stored, err := getMeta(ctx, tx, metaSessionToken)
		if err != nil {
			return err
		}
		if strings.TrimSpace(stored) == "" {
			return nil
		}
		if subtle.ConstantTimeCompare(sessionDigest(token), decodeDigest(stored)) != 1 {
			return nil
		}
		return setMetas(ctx, tx, map[string]string{metaSessionToken: "", metaSessionExpires: ""})
	})
}

func enforceLockout(ctx context.Context, tx queryable, now time.Time) error {
	untilRaw, err := getMeta(ctx, tx, metaLockoutUntil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(untilRaw) == "" {
		return nil
	}
	until, err := time.Parse(time.RFC3339, untilRaw)
	if err != nil {
		return fmt.Errorf("parse lockout deadline: %w", err)
	}
	if now.UTC().Before(until) {
		return ErrLocked
	}
	return nil
}

func recordFailure(ctx context.Context, tx queryable, now time.Time) error {
	attemptsRaw, err := getMeta(ctx, tx, metaFailedAttempts)
	if err != nil {
		return err
	}
	attempts := 0
	if attemptsRaw != "" {
		if attempts, err = strconv.Atoi(attemptsRaw); err != nil {
			attempts = 0
		}
	}
	attempts++
	updates := map[string]string{metaFailedAttempts: strconv.Itoa(attempts)}
	if attempts >= maxFailedAttempts {
		updates[metaLockoutUntil] = now.UTC().Add(lockoutDuration).Format(time.RFC3339)
	}
	return setMetas(ctx, tx, updates)
}

func resetFailuresAndIssueSession(ctx context.Context, tx queryable, now time.Time, ttl time.Duration, session *Session) error {
	tokenBytes := make([]byte, sessionTokenLength)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	session.Token = token
	session.ExpiresAt = now.UTC().Add(ttl)
	tokenDigest := sessionDigest(token)
	return setMetas(ctx, tx, map[string]string{
		metaFailedAttempts: "0",
		metaLockoutUntil:   "",
		metaSessionToken:   base64.RawStdEncoding.EncodeToString(tokenDigest),
		metaSessionExpires: session.ExpiresAt.Format(time.RFC3339),
	})
}

func getMeta(ctx context.Context, q queryable, key string) (string, error) {
	var value string
	err := q.QueryRowContext(ctx, `SELECT value FROM vertex_pro_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read meta %s: %w", key, err)
	}
	return value, nil
}

func setMetas(ctx context.Context, q queryable, values map[string]string) error {
	for key, value := range values {
		if _, err := q.ExecContext(ctx, `INSERT INTO vertex_pro_meta(key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return fmt.Errorf("write meta %s: %w", key, err)
		}
	}
	return nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, hashIterations, hashKeyLength)
	if err != nil {
		return "", fmt.Errorf("derive password hash: %w", err)
	}
	return fmt.Sprintf("%s$%d$%s$%s", hashAlgorithm, hashIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != hashAlgorithm {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(key, expected) == 1
}

func sessionDigest(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}

func decodeDigest(encoded string) []byte {
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return []byte(encoded)
	}
	return decoded
}

func validatePassword(password string) error {
	if len(password) < minPasswordLength {
		return fmt.Errorf("%w: minimum length %d", ErrWeakPassword, minPasswordLength)
	}
	return nil
}
