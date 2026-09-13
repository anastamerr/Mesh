package control

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkloadsValidateControllerResponse(t *testing.T) {
	nodeID := "12345678-1234-4234-8234-123456789abc"
	body := fmt.Sprintf(`[{"id":"22345678-1234-4234-8234-123456789abc","nodeId":%q,"executionEnvironmentId":"32345678-1234-4234-8234-123456789abc","name":"job","kind":"job",`+
		`"image":"example/job@sha256:%s","command":["run"],"resources":{"cpuMillis":500,"memoryBytes":134217728},`+
		`"inputCollectionId":null,"servicePort":null,"desiredState":"running","revision":1}]`, nodeID, strings.Repeat("a", 64))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
	defer server.Close()
	client, _ := New(server.URL)
	workloads, err := client.Workloads(context.Background(), nodeID, "credential")
	if err != nil || len(workloads) != 1 || workloads[0].Kind != "job" {
		t.Fatal("valid workload rejected", err)
	}
	body = strings.Replace(body, "@sha256:", ":latest", 1)
	if _, err := client.Workloads(context.Background(), nodeID, "credential"); err == nil {
		t.Fatal("mutable image tag accepted")
	}
}

func TestWorkloadRecordRequiresConsistentObservation(t *testing.T) {
	now := time.Now()
	exitCode := 0
	revision := uint64(1)
	outputID := strings.Repeat("c", 64)
	record := WorkloadRecord{
		Workload: Workload{WorkloadSpec: WorkloadSpec{
			ID: "22345678-1234-4234-8234-123456789abc", NodeID: "12345678-1234-4234-8234-123456789abc",
			Name: "job", Kind: "job", Image: "example/job@sha256:" + strings.Repeat("a", 64), Command: []string{"run"},
			Resources: WorkloadResources{CPUMillis: 500, MemoryBytes: 134217728}, DesiredState: "running",
		}, ExecutionEnvironmentID: "32345678-1234-4234-8234-123456789abc", Revision: 1},
		ObservedRevision: &revision, ObservedState: "succeeded", ExitCode: &exitCode, OutputCollectionID: &outputID,
		CreatedAt: now, UpdatedAt: now, ObservedAt: &now,
	}
	if !validWorkloadRecord(record) {
		t.Fatal("consistent terminal observation rejected")
	}
	record.ExitCode = nil
	if validWorkloadRecord(record) {
		t.Fatal("terminal observation without exit code accepted")
	}
}

func TestObserveWorkloadRequiresTypedTerminalResult(t *testing.T) {
	nodeID := "12345678-1234-4234-8234-123456789abc"
	workloadID := "22345678-1234-4234-8234-123456789abc"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"accepted":true}`) }))
	defer server.Close()
	client, _ := New(server.URL)
	if err := client.ObserveWorkload(context.Background(), nodeID, "credential", workloadID,
		WorkloadObservation{Revision: 1, State: "succeeded"}); err == nil {
		t.Fatal("terminal result without exit code accepted")
	}
	exit := 0
	outputID := strings.Repeat("c", 64)
	if err := client.ObserveWorkload(context.Background(), nodeID, "credential", workloadID,
		WorkloadObservation{Revision: 1, State: "succeeded", ExitCode: &exit, OutputCollectionID: &outputID}); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationTicketsAreBoundToWorkloadRoute(t *testing.T) {
	nodeID := "12345678-1234-4234-8234-123456789abc"
	workloadID := "22345678-1234-4234-8234-123456789abc"
	expires := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer credential" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"nodeId":%q,"route":%q,"servicePort":8080,"publicKeyFingerprint":%q,"expiresAt":%q,"token":%q}`,
			nodeID, "app-"+workloadID, strings.Repeat("d", 64), expires, strings.Repeat("e", 43))
	}))
	defer server.Close()
	client, _ := New(server.URL)
	device, err := client.ApplicationDeviceTicket(context.Background(), nodeID, "credential", workloadID)
	if err != nil || device.Route != "app-"+workloadID {
		t.Fatal("valid device application ticket rejected", err)
	}
	consumer, err := client.ApplicationConsumerTicket(context.Background(), "credential", workloadID)
	if err != nil || consumer.ServicePort != 8080 {
		t.Fatal("valid consumer application ticket rejected", err)
	}
}
