package control

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

var imageDigest = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:_-]*@sha256:[a-f0-9]{64}$`)
var applicationRoute = regexp.MustCompile(`^app-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type WorkloadResources struct {
	CPUMillis   int    `json:"cpuMillis"`
	MemoryBytes uint64 `json:"memoryBytes"`
}

type WorkloadSpec struct {
	ID                string            `json:"id"`
	NodeID            string            `json:"nodeId"`
	Name              string            `json:"name"`
	Kind              string            `json:"kind"`
	Image             string            `json:"image"`
	Command           []string          `json:"command"`
	Resources         WorkloadResources `json:"resources"`
	InputCollectionID *string           `json:"inputCollectionId"`
	ServicePort       *int              `json:"servicePort"`
	DesiredState      string            `json:"desiredState"`
}

type Workload struct {
	WorkloadSpec
	ExecutionEnvironmentID string `json:"executionEnvironmentId"`
	Revision               uint64 `json:"revision"`
}

type WorkloadRecord struct {
	Workload
	ObservedRevision   *uint64    `json:"observedRevision"`
	ObservedState      string     `json:"observedState"`
	ExitCode           *int       `json:"exitCode"`
	FailureCode        *string    `json:"failureCode"`
	OutputCollectionID *string    `json:"outputCollectionId"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	ObservedAt         *time.Time `json:"observedAt"`
}

type WorkloadObservation struct {
	Revision           uint64  `json:"revision"`
	State              string  `json:"state"`
	ExitCode           *int    `json:"exitCode"`
	FailureCode        *string `json:"failureCode"`
	OutputCollectionID *string `json:"outputCollectionId"`
}

type ApplicationRelayTicket struct {
	NodeID               string    `json:"nodeId"`
	Route                string    `json:"route"`
	ServicePort          int       `json:"servicePort"`
	PublicKeyFingerprint string    `json:"publicKeyFingerprint"`
	ExpiresAt            time.Time `json:"expiresAt"`
	Token                string    `json:"token"`
}

type ExecutionEnvironmentReport struct {
	Kind           string  `json:"kind"`
	Architecture   string  `json:"architecture"`
	Status         string  `json:"status"`
	RuntimeVersion *string `json:"runtimeVersion"`
}

type ExecutionEnvironment struct {
	ExecutionEnvironmentReport
	ID         string    `json:"id"`
	NodeID     string    `json:"nodeId"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	CreatedAt  time.Time `json:"createdAt"`
}

var runtimeVersion = regexp.MustCompile(`^[A-Za-z0-9.+-]{1,40}$`)

func (c *Client) ReportExecutionEnvironment(ctx context.Context, nodeID, credential string,
	report ExecutionEnvironmentReport) (ExecutionEnvironment, error) {
	var environment ExecutionEnvironment
	validStatus := (report.Status == "unavailable" && report.RuntimeVersion == nil) || (report.Status == "ready" &&
		report.RuntimeVersion != nil && runtimeVersion.MatchString(*report.RuntimeVersion))
	if !uuid.MatchString(nodeID) || report.Kind != "docker-linux" ||
		(report.Architecture != "amd64" && report.Architecture != "arm64") || !validStatus {
		return environment, ErrProtocol
	}
	if err := c.request(ctx, http.MethodPost, "/v1/nodes/"+url.PathEscape(nodeID)+"/execution-environments",
		credential, report, &environment); err != nil {
		return environment, err
	}
	if !validExecutionEnvironment(environment, nodeID) || environment.Kind != report.Kind ||
		environment.Architecture != report.Architecture || environment.Status != report.Status ||
		(environment.RuntimeVersion == nil) != (report.RuntimeVersion == nil) {
		return environment, ErrProtocol
	}
	if report.RuntimeVersion != nil && *environment.RuntimeVersion != *report.RuntimeVersion {
		return environment, ErrProtocol
	}
	return environment, nil
}

func validExecutionEnvironment(environment ExecutionEnvironment, nodeID string) bool {
	validStatus := environment.Status == "unavailable" && environment.RuntimeVersion == nil ||
		environment.Status == "ready" && environment.RuntimeVersion != nil && runtimeVersion.MatchString(*environment.RuntimeVersion)
	return uuid.MatchString(environment.ID) && environment.NodeID == nodeID && environment.Kind == "docker-linux" &&
		(environment.Architecture == "amd64" || environment.Architecture == "arm64") && validStatus &&
		!environment.LastSeenAt.IsZero() && !environment.CreatedAt.IsZero()
}

func (c *Client) ExecutionEnvironments(ctx context.Context, credential, nodeID string) ([]ExecutionEnvironment, error) {
	if !uuid.MatchString(nodeID) {
		return nil, ErrProtocol
	}
	var environments []ExecutionEnvironment
	if err := c.request(ctx, http.MethodGet, "/v1/nodes/"+url.PathEscape(nodeID)+"/execution-environments",
		credential, nil, &environments); err != nil {
		return nil, err
	}
	if environments == nil || len(environments) > 10 {
		return nil, ErrProtocol
	}
	for _, environment := range environments {
		if !validExecutionEnvironment(environment, nodeID) {
			return nil, ErrProtocol
		}
	}
	return environments, nil
}

func ValidWorkload(workload Workload, nodeID string) bool {
	return uuid.MatchString(workload.ExecutionEnvironmentID) && workload.Revision >= 1 &&
		validWorkloadSpec(workload.WorkloadSpec, nodeID)
}

func validWorkloadSpec(workload WorkloadSpec, nodeID string) bool {
	if !uuid.MatchString(workload.ID) || workload.NodeID != nodeID || len(workload.Name) < 1 || len(workload.Name) > 100 ||
		!imageDigest.MatchString(workload.Image) || len(workload.Command) < 1 || len(workload.Command) > 64 ||
		workload.Resources.CPUMillis < 100 || workload.Resources.CPUMillis > 64000 ||
		workload.Resources.MemoryBytes < 64*1024*1024 || workload.Resources.MemoryBytes > (1<<53)-1 {
		return false
	}
	for _, argument := range workload.Command {
		if len(argument) < 1 || len(argument) > 4096 {
			return false
		}
	}
	if workload.InputCollectionID != nil && !collectionID.MatchString(*workload.InputCollectionID) {
		return false
	}
	if workload.Kind == "job" {
		return workload.DesiredState == "running" && workload.ServicePort == nil
	}
	return workload.Kind == "application" && (workload.DesiredState == "running" || workload.DesiredState == "stopped") &&
		workload.ServicePort != nil && *workload.ServicePort >= 1 && *workload.ServicePort <= 65535
}

func (c *Client) Workloads(ctx context.Context, nodeID, credential string) ([]Workload, error) {
	if !uuid.MatchString(nodeID) {
		return nil, ErrProtocol
	}
	var workloads []Workload
	if err := c.request(ctx, http.MethodGet, "/v1/nodes/"+url.PathEscape(nodeID)+"/workloads", credential, nil, &workloads); err != nil {
		return nil, err
	}
	if workloads == nil || len(workloads) > 100 {
		return nil, ErrProtocol
	}
	for _, workload := range workloads {
		if !ValidWorkload(workload, nodeID) {
			return nil, ErrProtocol
		}
	}
	return workloads, nil
}

func (c *Client) ObserveWorkload(ctx context.Context, nodeID, credential, workloadID string, observation WorkloadObservation) error {
	if !uuid.MatchString(nodeID) || !uuid.MatchString(workloadID) || observation.Revision < 1 {
		return ErrProtocol
	}
	terminal := observation.State == "succeeded" || observation.State == "failed"
	if terminal != (observation.ExitCode != nil) || (observation.State == "failed") != (observation.FailureCode != nil) {
		return ErrProtocol
	}
	if observation.State != "pulling" && observation.State != "starting" && observation.State != "running" &&
		observation.State != "exporting" && observation.State != "succeeded" && observation.State != "failed" &&
		observation.State != "stopped" {
		return ErrProtocol
	}
	if observation.ExitCode != nil && (*observation.ExitCode < 0 || *observation.ExitCode > 255) {
		return ErrProtocol
	}
	if observation.FailureCode != nil && *observation.FailureCode != "image-unavailable" &&
		*observation.FailureCode != "invalid-runtime" && *observation.FailureCode != "resource-unavailable" &&
		*observation.FailureCode != "runtime-failure" {
		return ErrProtocol
	}
	if (observation.State == "succeeded") != (observation.OutputCollectionID != nil) ||
		observation.OutputCollectionID != nil && !collectionID.MatchString(*observation.OutputCollectionID) {
		return ErrProtocol
	}
	err := c.postAccepted(ctx, "/v1/nodes/"+url.PathEscape(nodeID)+"/workloads/"+
		url.PathEscape(workloadID)+"/observations", credential, observation)
	var api *APIError
	if errors.As(err, &api) && api.Status == http.StatusConflict {
		return nil
	}
	return err
}

func validApplicationRelayTicket(ticket ApplicationRelayTicket, nodeID, workloadID string) bool {
	return ticket.NodeID == nodeID && ticket.Route == "app-"+workloadID && applicationRoute.MatchString(ticket.Route) &&
		ticket.ServicePort >= 1 && ticket.ServicePort <= 65535 && collectionID.MatchString(ticket.PublicKeyFingerprint) &&
		grantToken.MatchString(ticket.Token) && ticket.ExpiresAt.After(time.Now())
}

func (c *Client) ApplicationDeviceTicket(ctx context.Context, nodeID, credential, workloadID string) (ApplicationRelayTicket, error) {
	var ticket ApplicationRelayTicket
	if !uuid.MatchString(nodeID) || !uuid.MatchString(workloadID) {
		return ticket, ErrProtocol
	}
	err := c.request(ctx, http.MethodPost, "/v1/nodes/"+url.PathEscape(nodeID)+"/workloads/"+
		url.PathEscape(workloadID)+"/relay-ticket", credential, nil, &ticket)
	if err == nil && !validApplicationRelayTicket(ticket, nodeID, workloadID) {
		err = ErrProtocol
	}
	return ticket, err
}

func (c *Client) ApplicationConsumerTicket(ctx context.Context, operator, workloadID string) (ApplicationRelayTicket, error) {
	var ticket ApplicationRelayTicket
	if !uuid.MatchString(workloadID) {
		return ticket, ErrProtocol
	}
	err := c.request(ctx, http.MethodPost, "/v1/workloads/"+url.PathEscape(workloadID)+"/connection-ticket",
		operator, nil, &ticket)
	if err == nil && !validApplicationRelayTicket(ticket, ticket.NodeID, workloadID) {
		err = ErrProtocol
	}
	return ticket, err
}

func (c *Client) CreateWorkload(ctx context.Context, operator string, specification WorkloadSpec) (WorkloadRecord, error) {
	var record WorkloadRecord
	if !validWorkloadSpec(specification, specification.NodeID) {
		return record, ErrProtocol
	}
	if err := c.request(ctx, http.MethodPost, "/v1/workloads", operator, specification, &record); err != nil {
		return record, err
	}
	if !validWorkloadRecord(record) || record.ID != specification.ID || record.NodeID != specification.NodeID {
		return record, ErrProtocol
	}
	return record, nil
}

func (c *Client) ListWorkloads(ctx context.Context, operator string) ([]WorkloadRecord, error) {
	var records []WorkloadRecord
	if err := c.request(ctx, http.MethodGet, "/v1/workloads", operator, nil, &records); err != nil {
		return nil, err
	}
	if records == nil || len(records) > 100 {
		return nil, ErrProtocol
	}
	for _, record := range records {
		if !validWorkloadRecord(record) {
			return nil, ErrProtocol
		}
	}
	return records, nil
}

func (c *Client) SetWorkloadState(ctx context.Context, operator, id, desiredState string) (WorkloadRecord, error) {
	var record WorkloadRecord
	if !uuid.MatchString(id) || (desiredState != "running" && desiredState != "stopped") {
		return record, ErrProtocol
	}
	err := c.request(ctx, http.MethodPost, "/v1/workloads/"+url.PathEscape(id)+"/state", operator,
		struct {
			DesiredState string `json:"desiredState"`
		}{desiredState}, &record)
	if err == nil && (!validWorkloadRecord(record) || record.ID != id || record.DesiredState != desiredState) {
		err = ErrProtocol
	}
	return record, err
}

func validWorkloadRecord(record WorkloadRecord) bool {
	if !ValidWorkload(record.Workload, record.NodeID) || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() ||
		record.ObservedState != "pending" && record.ObservedState != "pulling" && record.ObservedState != "starting" &&
			record.ObservedState != "running" && record.ObservedState != "succeeded" && record.ObservedState != "failed" &&
			record.ObservedState != "exporting" && record.ObservedState != "stopped" {
		return false
	}
	terminal := record.ObservedState == "succeeded" || record.ObservedState == "failed"
	if terminal != (record.ExitCode != nil) || (record.ObservedState == "failed") != (record.FailureCode != nil) ||
		(record.ObservedState == "succeeded") != (record.OutputCollectionID != nil) ||
		(record.ObservedState == "pending") != (record.ObservedRevision == nil && record.ObservedAt == nil) {
		return false
	}
	if record.ExitCode != nil && (*record.ExitCode < 0 || *record.ExitCode > 255) {
		return false
	}
	if record.FailureCode != nil && *record.FailureCode != "image-unavailable" &&
		*record.FailureCode != "invalid-runtime" && *record.FailureCode != "resource-unavailable" &&
		*record.FailureCode != "runtime-failure" {
		return false
	}
	if record.OutputCollectionID != nil && !collectionID.MatchString(*record.OutputCollectionID) {
		return false
	}
	return record.ObservedRevision == nil || *record.ObservedRevision >= 1 && *record.ObservedRevision <= record.Revision
}
