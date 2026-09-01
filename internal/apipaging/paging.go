package apipaging

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrInvalidCursor    = errors.New("opaque cursor is invalid")
	ErrRevisionConflict = errors.New("resource revision conflict")
)

// Cursor 是稳定排序键 + 唯一 ID 的元组。分页基于键元组而非偏移量，
// 因此翻页间隙内的插入/删除不会造成重复或跳漏；删除已翻过页的条目
// 也不影响后续页。HMAC 签名防止客户端伪造排序键。
type Cursor struct {
	SortKey string
	ID      string
}

var signingKey = []byte("vertex-pro-cursor-v1")

func Encode(cursor Cursor) string {
	raw, _ := json.Marshal(cursor)
	mac := hmac.New(sha256.New, signingKey)
	mac.Write(raw)
	signature := mac.Sum(nil)
	payload := append(raw, signature...)
	return base64.RawURLEncoding.EncodeToString(payload)
}

func Decode(encoded string) (Cursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) < sha256.Size+2 {
		return Cursor{}, fmt.Errorf("%w: malformed cursor", ErrInvalidCursor)
	}
	raw := payload[:len(payload)-sha256.Size]
	signature := payload[len(payload)-sha256.Size:]
	mac := hmac.New(sha256.New, signingKey)
	mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return Cursor{}, fmt.Errorf("%w: cursor signature mismatch", ErrInvalidCursor)
	}
	var cursor Cursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return Cursor{}, fmt.Errorf("%w: cursor payload invalid", ErrInvalidCursor)
	}
	return cursor, nil
}

// CheckRevision 是乐观并发守卫：expected 为客户端 If-Match 携带的修订号，
// actual 为存储当前值。不匹配返回冲突错误（API 层映射 REVISION_CONFLICT 409）。
func CheckRevision(expected, actual int64) error {
	if expected != actual {
		return fmt.Errorf("%w: expected %d actual %d", ErrRevisionConflict, expected, actual)
	}
	return nil
}
