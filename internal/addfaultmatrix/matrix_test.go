package addfaultmatrix

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/db"
)

// TestP0FaultCasesDeterministicAcrossThreeRuns 执行 Gate H 的最后一条：
// P0 故障用例连续运行三次均得到相同数据库终态。
func TestP0FaultCasesDeterministicAcrossThreeRuns(t *testing.T) {
	passThrough := func(string) error { return nil }
	killAtRecover := func(stage string) error {
		if stage == "recover" {
			return errKilled
		}
		return nil
	}
	for _, testCase := range Manifest() {
		t.Run(testCase.ID, func(t *testing.T) {
			digests := make([]string, 0, 3)
			for run := 0; run < 3; run++ {
				database := openMigrated(t)
				testCase.Seed(t, database)
				hook := passThrough
				if run == 2 {
					// 第三次运行在首个注入点模拟 kill -9，随后从中断状态继续收敛。
					hook = killAtRecover
				}
				driveErr := testCase.Drive(context.Background(), database, hook)
				if driveErr != nil && !errors.Is(driveErr, errKilled) {
					t.Fatalf("run %d drive: %v", run, driveErr)
				}
				if errors.Is(driveErr, errKilled) {
					// 进程"重启"后从持久化状态重新驱动到收敛。
					if err := testCase.Drive(context.Background(), database, passThrough); err != nil {
						t.Fatalf("run %d resume: %v", run, err)
					}
				}
				digest, err := Oracle(database)
				if err != nil {
					t.Fatalf("run %d oracle: %v", run, err)
				}
				digests = append(digests, digest)
				database.Close()
				// Windows 上 modernc sqlite 释放句柄略晚于 Close 返回，稍候再交给 TempDir 清理。
				time.Sleep(25 * time.Millisecond)
			}
			if digests[0] != digests[1] || digests[1] != digests[2] {
				t.Fatalf("terminal states differ across runs: %s / %s / %s", digests[0], digests[1], digests[2])
			}
		})
	}
}

var errKilled = &killError{}

type killError struct{}

func (*killError) Error() string { return "simulated kill -9" }

func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "fault-matrix.db"), MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := db.DefaultRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Apply(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	return database
}
