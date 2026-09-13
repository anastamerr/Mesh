package stream

import "bytes"

// LimitedBuffer drains command output while retaining at most Remaining bytes.
// Truncation is reported separately so the child can finish without a pipe error.
type LimitedBuffer struct {
	Buffer    bytes.Buffer
	Remaining int
	Truncated bool
}

func (output *LimitedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	if len(data) > output.Remaining {
		data = data[:output.Remaining]
		output.Truncated = true
	}
	output.Remaining -= len(data)
	_, _ = output.Buffer.Write(data)
	return written, nil
}
