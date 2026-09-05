package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupAndEncryptedRestoreRoundTrip(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "source.db")
	config := Config{Path: databasePath}
	database, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE drill_marker(id INTEGER PRIMARY KEY, note TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO drill_marker(note) VALUES ('restore drill marker')`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := BackupDatabase(context.Background(), config, backupPath); err != nil {
		t.Fatal(err)
	}

	// 明文备份先行 Restore drill：还原到另一个路径并校验内容。
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	if err := Restore(Config{Path: restoredPath}, backupPath); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(context.Background(), Config{Path: restoredPath})
	if err != nil {
		t.Fatal(err)
	}
	var note string
	if err := restored.QueryRow(`SELECT note FROM drill_marker WHERE id = 1`).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note != "restore drill marker" {
		t.Fatalf("note = %q", note)
	}
	restored.Close()

	// 加密备份：错误密钥解密必须失败且不产生输出文件。
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index)
	}
	encryptedPath := filepath.Join(t.TempDir(), "backup.enc")
	if err := EncryptFile(backupPath, encryptedPath, key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(encryptedPath); err != nil {
		t.Fatal(err)
	}
	wrongKey := make([]byte, 32)
	decryptedWrong := filepath.Join(t.TempDir(), "wrong.db")
	if err := DecryptFile(encryptedPath, decryptedWrong, wrongKey); err == nil {
		t.Fatal("decrypt with a wrong key must fail")
	}
	if _, err := os.Stat(decryptedWrong); !os.IsNotExist(err) {
		t.Fatal("failed decryption must not write the destination")
	}

	// 正确密钥解密后内容一致。
	decryptedPath := filepath.Join(t.TempDir(), "decrypted.db")
	if err := DecryptFile(encryptedPath, decryptedPath, key); err != nil {
		t.Fatal(err)
	}
	decrypted, err := Open(context.Background(), Config{Path: decryptedPath})
	if err != nil {
		t.Fatal(err)
	}
	defer decrypted.Close()
	if err := decrypted.QueryRow(`SELECT note FROM drill_marker WHERE id = 1`).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note != "restore drill marker" {
		t.Fatalf("decrypted note = %q", note)
	}
}

func TestLoadKeyRejectsBadInput(t *testing.T) {
	if _, err := LoadKey("not-hex"); err == nil {
		t.Fatal("non-hex key accepted")
	}
	if _, err := LoadKey("aabb"); err == nil {
		t.Fatal("short key accepted")
	}
	valid := strings.Repeat("ab", 32)
	if _, err := LoadKey(valid); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
}
