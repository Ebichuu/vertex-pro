package db

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// BackupDatabase 创建 SQLite 数据库的冷备份：先做 WAL checkpoint 并关闭
// 连接，再原子复制数据库文件到目标路径。备份文件权限为 0600。
func BackupDatabase(ctx context.Context, config Config, backupPath string) error {
	database, err := Open(ctx, config)
	if err != nil {
		return err
	}
	if err := checkpointAndClose(ctx, database, config.Path); err != nil {
		return err
	}
	return copyDatabaseFile(config.Path, backupPath)
}

// LoadKey 从环境变量值解析 64 位十六进制字符（32 字节）的备份密钥。密钥
// 只经环境变量传入，不写入配置文件或日志。
func LoadKey(envValue string) ([]byte, error) {
	raw, err := hex.DecodeString(envValue)
	if err != nil {
		return nil, errors.New("backup key must be 64 hex characters (32 bytes)")
	}
	if len(raw) != 32 {
		return nil, errors.New("backup key must decode to exactly 32 bytes")
	}
	return raw, nil
}

// EncryptFile 用 AES-256-GCM 把 sourcePath 加密写入 destinationPath：
// 随机 nonce 前置存储，认证标签保证密文完整性。输出权限 0600。
func EncryptFile(sourcePath, destinationPath string, key []byte) error {
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}
	plaintext, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read plaintext: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	return writeAtomically(destinationPath, aead.Seal(nonce, nonce, plaintext, nil))
}

// DecryptFile 把 EncryptFile 产生的密文解密还原到 destinationPath（0600）。
// 认证失败即返回错误，不会写出被篡改的内容。
func DecryptFile(sourcePath, destinationPath string, key []byte) error {
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}
	ciphertext, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("read ciphertext: %w", err)
	}
	if len(ciphertext) < aead.NonceSize() {
		return errors.New("ciphertext shorter than nonce")
	}
	nonce, sealed := ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return fmt.Errorf("decrypt backup: %w", err)
	}
	return writeAtomically(destinationPath, plaintext)
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("backup key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// writeAtomically 按 0600 权限经临时文件写入后 rename，避免半写产物。
func writeAtomically(path string, payload []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".backup-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("rename backup into place: %w", err)
	}
	return nil
}
