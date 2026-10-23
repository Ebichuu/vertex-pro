package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeInstancesAreStrictAndBindEachStorage(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"two instances", `{"instances":[{"instance_id":"2","qui_url":"http://qui.test","api_key_file":"control.txt","proxy_key_file":"two.txt","storage":{"storage_target_id":"two","path":"/downloads"}},{"instance_id":"7","qui_url":"http://qui.test","api_key_file":"control.txt","proxy_key_file":"seven.txt","storage":{"storage_target_id":"seven","path":"/downloads"}}]}`, true},
		{"unknown option", `{"instances":[],"enable_all":true}`, false},
		{"empty", `{"instances":[]}`, false},
		{"raw credential", `{"instances":[{"instance_id":"2","api_key":"must-not-be-accepted"}]}`, false},
		{"wrong storage instance", `{"instances":[{"instance_id":"2","qui_url":"http://qui.test","api_key_file":"control.txt","proxy_key_file":"two.txt","storage":{"instance_id":"7","storage_target_id":"two","path":"/downloads"}}]}`, false},
		{"trailing document", `{"instances":[]} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "instances.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			configs, err := readRuntimeInstances(path, t.TempDir(), time.Second, time.Second, true, []string{"site.test"})
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid configuration accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(configs) != 2 || configs[0].DispatchStage.Storage.InstanceID != "2" || configs[1].DispatchStage.Storage.InstanceID != "7" || configs[0].ProxyKey.Key == configs[1].ProxyKey.Key {
				t.Fatal("per-instance configuration was lost")
			}
		})
	}
}
