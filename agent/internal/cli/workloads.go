package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"mesh.local/agent/internal/control"
)

type workloadArguments []string

func (arguments *workloadArguments) String() string { return fmt.Sprint([]string(*arguments)) }
func (arguments *workloadArguments) Set(value string) error {
	if value == "" || len(value) > 4096 || len(*arguments) >= 64 {
		return errors.New("each --arg must contain 1 to 4096 bytes; at most 64 are allowed")
	}
	*arguments = append(*arguments, value)
	return nil
}

func newUUID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", errors.New("cannot generate workload identity")
	}
	data[6] = data[6]&0x0f | 0x40
	data[8] = data[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", data[0:4], data[4:6], data[6:8], data[8:10], data[10:16]), nil
}

func workloadOperatorCommand(ctx context.Context, args []string, input io.Reader, output, logs io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: mesh-agent workload create-job | create-app | list | environments | start | stop | connect")
	}
	action := args[0]
	if action != "create-job" && action != "create-app" && action != "list" && action != "environments" && action != "start" && action != "stop" && action != "connect" {
		return errors.New("unknown workload command")
	}
	flags := flag.NewFlagSet("workload "+action, flag.ContinueOnError)
	flags.SetOutput(logs)
	var controllerOrigin, node, id, name, image, collection, listenAddress, relayCA string
	var cpuMillis int
	var memoryMiB uint64
	var port int
	var operatorStdin, asJSON, stopped bool
	var command workloadArguments
	flags.StringVar(&controllerOrigin, "controller", "http://127.0.0.1:3000", "controller origin")
	flags.BoolVar(&operatorStdin, "operator-stdin", false, "read operator credential from stdin")
	flags.BoolVar(&asJSON, "json", false, "emit machine-readable output")
	if action == "create-job" || action == "create-app" {
		flags.StringVar(&node, "node", "", "node name or full ID")
		flags.StringVar(&id, "id", "", "UUID for idempotent creation; generated when omitted")
		flags.StringVar(&name, "name", "", "workload display name")
		flags.StringVar(&image, "image", "", "digest-pinned image reference")
		flags.IntVar(&cpuMillis, "cpu", 1000, "CPU limit in millicores")
		flags.Uint64Var(&memoryMiB, "memory-mib", 512, "memory limit in MiB")
		flags.StringVar(&collection, "input", "", "confirmed input collection ID")
		flags.Var(&command, "arg", "one command argument; repeat in execution order")
		if action == "create-app" {
			flags.IntVar(&port, "port", 0, "application container port")
			flags.BoolVar(&stopped, "stopped", false, "create without starting")
		}
	} else if action == "environments" {
		flags.StringVar(&node, "node", "", "node name or full ID")
	} else if action == "start" || action == "stop" {
		flags.StringVar(&id, "id", "", "workload UUID")
	} else if action == "connect" {
		flags.StringVar(&id, "id", "", "application workload UUID")
		flags.StringVar(&listenAddress, "listen", "127.0.0.1:8080", "local loopback address")
		flags.StringVar(&relayCA, "relay-ca", "", "optional private relay CA PEM")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || !operatorStdin {
		return errors.New("use --operator-stdin and named options; npm run mesh supplies the credential")
	}
	credential, err := readOperatorCredential(input)
	if err != nil {
		return err
	}
	client, err := control.New(controllerOrigin)
	if err != nil {
		return err
	}
	if action == "list" {
		return listWorkloads(ctx, client, credential, asJSON, output)
	}
	if action == "connect" {
		if id == "" {
			return errors.New("connect requires --id")
		}
		return serveApplication(ctx, client, credential, id, listenAddress, relayCA, output, logs)
	}
	if action == "environments" {
		if node == "" {
			return errors.New("environments requires --node")
		}
		nodeID, err := client.ResolveNode(ctx, credential, node)
		if err != nil {
			return err
		}
		environments, err := client.ExecutionEnvironments(ctx, credential, nodeID)
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(output).Encode(environments)
		}
		writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "KIND\tARCHITECTURE\tSTATUS\tVERSION\tLAST SEEN")
		for _, environment := range environments {
			version := "-"
			if environment.RuntimeVersion != nil {
				version = *environment.RuntimeVersion
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", environment.Kind, environment.Architecture,
				environment.Status, version, environment.LastSeenAt.Format(time.RFC3339))
		}
		if len(environments) == 0 {
			fmt.Fprintln(writer, "No execution environment reported. Start the agent with --compute.")
		}
		return writer.Flush()
	}
	if action == "start" || action == "stop" {
		if id == "" {
			return errors.New("start and stop require --id")
		}
		state := "running"
		if action == "stop" {
			state = "stopped"
		}
		record, err := client.SetWorkloadState(ctx, credential, id, state)
		if err != nil {
			return err
		}
		return printWorkload(record, asJSON, output)
	}
	if node == "" || name == "" || image == "" || len(command) == 0 {
		return errors.New("creation requires --node, --name, --image, and at least one --arg")
	}
	if memoryMiB > ((1<<53)-1)/(1024*1024) {
		return errors.New("memory limit is too large")
	}
	if id == "" {
		id, err = newUUID()
		if err != nil {
			return err
		}
	}
	nodeID, err := client.ResolveNode(ctx, credential, node)
	if err != nil {
		return err
	}
	var inputCollection *string
	if collection != "" {
		inputCollection = &collection
	}
	var servicePort *int
	kind := "job"
	desiredState := "running"
	if action == "create-app" {
		kind = "application"
		servicePort = &port
		if stopped {
			desiredState = "stopped"
		}
	}
	record, err := client.CreateWorkload(ctx, credential, control.WorkloadSpec{ID: id, NodeID: nodeID, Name: name,
		Kind: kind, Image: image, Command: command, Resources: control.WorkloadResources{CPUMillis: cpuMillis,
			MemoryBytes: memoryMiB * 1024 * 1024}, InputCollectionID: inputCollection, ServicePort: servicePort,
		DesiredState: desiredState})
	if err != nil {
		return err
	}
	return printWorkload(record, asJSON, output)
}

func printWorkload(record control.WorkloadRecord, asJSON bool, output io.Writer) error {
	if asJSON {
		return json.NewEncoder(output).Encode(record)
	}
	line := fmt.Sprintf("%s %q (%s), desired=%s observed=%s revision=%d",
		record.Kind, record.Name, record.ID, record.DesiredState, record.ObservedState, record.Revision)
	if record.OutputCollectionID != nil {
		line += ", output=" + *record.OutputCollectionID
	}
	_, err := fmt.Fprintln(output, line)
	return err
}

func listWorkloads(ctx context.Context, client *control.Client, credential string, asJSON bool, output io.Writer) error {
	records, err := client.ListWorkloads(ctx, credential)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(output).Encode(records)
	}
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "NAME\tKIND\tDESIRED\tOBSERVED\tREVISION\tOUTPUT\tID")
	for _, record := range records {
		outputID := "-"
		if record.OutputCollectionID != nil {
			outputID = *record.OutputCollectionID
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", record.Name, record.Kind, record.DesiredState,
			record.ObservedState, strconv.FormatUint(record.Revision, 10), outputID, record.ID)
	}
	if len(records) == 0 {
		fmt.Fprintln(writer, "No workloads yet. Use job or app to create one.")
	}
	return writer.Flush()
}
