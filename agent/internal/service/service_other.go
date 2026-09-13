//go:build !windows

package service

import (
	"context"
	"errors"
)

func Run([]string, Runner) error {
	return errors.New("Mesh service hosting is available only on Windows")
}

func Install(context.Context, string, string, []string) error {
	return errors.New("Mesh service installation is available only on Windows")
}

func Start(context.Context) error {
	return errors.New("Mesh service management is available only on Windows")
}

func Stop(context.Context) error {
	return errors.New("Mesh service management is available only on Windows")
}

func Uninstall(context.Context) error {
	return errors.New("Mesh service management is available only on Windows")
}

func Status(context.Context) (string, error) {
	return "", errors.New("Mesh service management is available only on Windows")
}
