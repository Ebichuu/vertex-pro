package dispatchstage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vertex-pro/vertex-pro/internal/racejobstore"
	"github.com/vertex-pro/vertex-pro/internal/ruleaggregate"
	"github.com/vertex-pro/vertex-pro/internal/torrentdownload"
)

func TestStageReusesFrozenArtifactAndFailsClosedOnCorruption(t *testing.T) {
	for _, preloaded := range []bool{false, true} {
		name := "legacy"
		if preloaded {
			name = "frozen"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC()
			body := []byte("d4:infod6:lengthi42e4:name8:file.bin12:piece lengthi16384e6:pieces20:01234567890123456789ee")
			var downloads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				w.Header().Set("Content-Type", "application/x-bittorrent")
				w.Write(body)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			downloader, err := torrentdownload.New(torrentdownload.Config{AllowedHosts: []string{target.Host}})
			if err != nil {
				t.Fatal(err)
			}
			database := deadlineDatabase(t)
			seedDeadlineJob(t, database, body, server.URL, now.Add(time.Hour), ruleaggregate.Result{Winner: ruleaggregate.Match{RuleID: "rule", TargetMode: "FIXED_INSTANCE", TargetInstanceID: "qb-1"}})
			if preloaded {
				if err := racejobstore.SaveLegacyArtifact(ctx, database, "job-1", body, now); err != nil {
					t.Fatal(err)
				}
				server.Close() // New jobs no longer need their original URL at dispatch.
			}
			collected := 0
			sender := &deadlineSender{}
			config := Config{Storage: StorageConfig{InstanceID: "qb-1", StorageTargetID: "target-1", Path: "/data"}, Downloader: downloader, Sender: sender, Now: func() time.Time { return now }, Collect: func(context.Context) (Evidence, func(), error) {
				collected++
				return Evidence{}, func() {}, errors.New("fixture unavailable")
			}}
			for i := 0; i < 2; i++ {
				stage, err := New(database, config)
				if err != nil {
					t.Fatal(err)
				}
				_, failures, err := stage.Run(ctx)
				if err != nil || len(failures) != 1 {
					t.Fatalf("failures=%v err=%v", failures, err)
				}
			}
			want := int32(1)
			if preloaded {
				want = 0
			}
			if downloads.Load() != want || collected != 2 || sender.calls != 0 {
				t.Fatalf("downloads=%d collected=%d sends=%d", downloads.Load(), collected, sender.calls)
			}
			if _, err := database.Exec(`UPDATE job_torrent_artifacts SET body=? WHERE job_id='job-1'`, []byte("corrupt")); err != nil {
				t.Fatal(err)
			}
			stage, _ := New(database, config)
			_, failures, err := stage.Run(ctx)
			if err != nil || len(failures) != 1 || collected != 2 || downloads.Load() != want || sender.calls != 0 {
				t.Fatalf("corruption was not stopped: %v %v", failures, err)
			}
		})
	}
}
