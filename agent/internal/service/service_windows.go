//go:build windows

package service

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

func Run(arguments []string, runner Runner) error {
	serviceProcess, err := svc.IsWindowsService()
	if err != nil {
		return errors.New("cannot determine Windows service context")
	}
	if !serviceProcess {
		return errors.New("service run is reserved for the Windows Service Control Manager")
	}
	log, logError := eventlog.Open(Name)
	var output io.Writer = io.Discard
	if logError == nil {
		defer log.Close()
		output = &eventWriter{log: log}
	}
	return svc.Run(Name, &handler{arguments: arguments, runner: runner, logs: output})
}

type handler struct {
	arguments []string
	runner    Runner
	logs      io.Writer
}

func (handler *handler) Execute(_ []string, requests <-chan svc.ChangeRequest,
	statuses chan<- svc.Status) (bool, uint32) {
	statuses <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- handler.runner(ctx, handler.logs) }()
	running := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	statuses <- running
	for {
		select {
		case err := <-done:
			if err != nil {
				_, _ = handler.logs.Write([]byte("Mesh service stopped after an unrecoverable runner error: " + err.Error()))
				return true, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				statuses <- running
			case svc.Stop, svc.Shutdown:
				statuses <- svc.Status{State: svc.StopPending}
				cancel()
				if err := <-done; err != nil {
					_, _ = handler.logs.Write([]byte("Mesh service shutdown reported: " + err.Error()))
				}
				return false, 0
			}
		}
	}
}

type eventWriter struct {
	log   *eventlog.Log
	mutex sync.Mutex
}

func (writer *eventWriter) Write(data []byte) (int, error) {
	message := strings.TrimSpace(string(data))
	if message == "" {
		return len(data), nil
	}
	writer.mutex.Lock()
	err := writer.log.Info(1, message)
	writer.mutex.Unlock()
	return len(data), err
}

func Install(_ context.Context, account, password string, runArguments []string) error {
	executable, err := os.Executable()
	if err != nil {
		return errors.New("cannot resolve the Mesh executable")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("Mesh service executable must be a regular file")
	}
	manager, err := mgr.Connect()
	if err != nil {
		return errors.New("cannot connect to Windows service management; run from an elevated terminal")
	}
	defer manager.Disconnect()
	if existing, openError := manager.OpenService(Name); openError == nil {
		existing.Close()
		return errors.New("Mesh service is already installed")
	}
	if err := eventlog.InstallAsEventCreate(Name, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		return errors.New("cannot install the Mesh event log source")
	}
	arguments := append([]string{"service", "run"}, runArguments...)
	installed, err := manager.CreateService(Name, executable, mgr.Config{DisplayName: "Mesh Personal Cloud",
		Description: "Provides private storage and compute from this device.", StartType: mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal, ServiceStartName: account, Password: password, DelayedAutoStart: true}, arguments...)
	if err != nil {
		_ = eventlog.Remove(Name)
		return errors.New("cannot install Mesh service; verify elevation and account credentials")
	}
	defer installed.Close()
	if err := installed.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Minute},
	}, 86400); err != nil {
		_ = installed.Delete()
		_ = eventlog.Remove(Name)
		return errors.New("cannot configure Mesh service recovery")
	}
	return nil
}

func Start(ctx context.Context) error {
	service, manager, err := open()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return errors.New("cannot query Mesh service")
	}
	if status.State == svc.Running {
		return nil
	}
	if err := service.Start(); err != nil {
		return errors.New("cannot start Mesh service")
	}
	return waitForState(ctx, service, svc.Running)
}

func Stop(ctx context.Context) error {
	service, manager, err := open()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return errors.New("cannot query Mesh service")
	}
	if status.State == svc.Stopped {
		return nil
	}
	if _, err := service.Control(svc.Stop); err != nil {
		return errors.New("cannot stop Mesh service")
	}
	return waitForState(ctx, service, svc.Stopped)
}

func Uninstall(ctx context.Context) error {
	if err := Stop(ctx); err != nil {
		return err
	}
	service, manager, err := open()
	if err != nil {
		return err
	}
	defer manager.Disconnect()
	defer service.Close()
	if err := service.Delete(); err != nil {
		return errors.New("cannot remove Mesh service")
	}
	_ = eventlog.Remove(Name)
	return nil
}

func Status(_ context.Context) (string, error) {
	service, manager, err := open()
	if err != nil {
		return "", err
	}
	defer manager.Disconnect()
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return "", errors.New("cannot query Mesh service")
	}
	switch status.State {
	case svc.Stopped:
		return "stopped", nil
	case svc.StartPending:
		return "starting", nil
	case svc.StopPending:
		return "stopping", nil
	case svc.Running:
		return "running", nil
	case svc.Paused, svc.PausePending, svc.ContinuePending:
		return "paused", nil
	default:
		return "unknown", nil
	}
}

func open() (*mgr.Service, *mgr.Mgr, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return nil, nil, errors.New("cannot connect to Windows service management; run from an elevated terminal")
	}
	service, err := manager.OpenService(Name)
	if err != nil {
		manager.Disconnect()
		return nil, nil, errors.New("Mesh service is not installed")
	}
	return service, manager, nil
}

func waitForState(ctx context.Context, service *mgr.Service, expected svc.State) error {
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return errors.New("timed out waiting for Mesh service")
		case <-timer.C:
			status, err := service.Query()
			if err != nil {
				return errors.New("cannot query Mesh service")
			}
			if status.State == expected {
				return nil
			}
		}
	}
}
