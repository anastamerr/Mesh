package service

import (
	"context"
	"io"
)

const Name = "MeshPersonalCloud"

type Runner func(context.Context, io.Writer) error
