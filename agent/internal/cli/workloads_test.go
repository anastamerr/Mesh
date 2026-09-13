package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"mesh.local/agent/internal/control"
)

func TestWorkloadCLIResolvesNodeAndCreatesTypedJob(t *testing.T) {
	credential := strings.Repeat("k", 40)
	nodeID := "12345678-1234-4234-8234-123456789abc"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+credential {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/nodes" {
			fmt.Fprintf(w, `[{"id":%q,"name":"Old laptop"}]`, nodeID)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/workloads" {
			data, err := io.ReadAll(r.Body)
			if err != nil || bytes.Contains(data, []byte(`"revision"`)) {
				t.Error("creation leaked agent-only revision")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var specification control.WorkloadSpec
			if err := json.Unmarshal(data, &specification); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			now := time.Now()
			record := control.WorkloadRecord{Workload: control.Workload{WorkloadSpec: specification,
				ExecutionEnvironmentID: "32345678-1234-4234-8234-123456789abc", Revision: 1},
				ObservedState: "pending", CreatedAt: now, UpdatedAt: now}
			_ = json.NewEncoder(w).Encode(record)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	var output bytes.Buffer
	err := workloadOperatorCommand(context.Background(), []string{"create-job", "--controller", server.URL,
		"--operator-stdin", "--node", "Old laptop", "--name", "Transcode", "--image",
		"example/job@sha256:" + strings.Repeat("a", 64), "--cpu", "2000", "--memory-mib", "256",
		"--arg", "convert", "--arg", "/mesh/input/video.mp4"}, strings.NewReader(credential), &output, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `job "Transcode"`) {
		t.Fatal("friendly result missing", output.String())
	}
}

func TestGeneratedWorkloadIDIsUUIDVersionFour(t *testing.T) {
	id, err := newUUID()
	matched := regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`).MatchString(id)
	if err != nil || !matched {
		t.Fatal("generated invalid UUID", id, err)
	}
}
