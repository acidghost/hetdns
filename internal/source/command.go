package source

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/netip"
	"os/exec"
	"time"

	"github.com/acidghost/hetdns/internal/config"
)

// Command obtains an address by executing an argv directly, without a shell.
type Command struct {
	id             string
	family         Family
	argv           []string
	timeout        time.Duration
	allowNonPublic bool
}

// NewCommand constructs a command source.
func NewCommand(id string, spec config.Source) (*Command, error) {
	if len(spec.Argv) == 0 {
		return nil, errors.New("empty command")
	}
	timeout := spec.Timeout.Duration
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	command := &Command{
		id:             id,
		family:         familyOf(spec.Family),
		argv:           append([]string(nil), spec.Argv...),
		timeout:        timeout,
		allowNonPublic: spec.AllowNonPublic,
	}
	return command, nil
}

func (s *Command) ID() string     { return s.id }
func (s *Command) Family() Family { return s.family }

// Fetch runs the configured executable with a minimal environment and bounded output.
func (s *Command) Fetch(ctx context.Context) (netip.Addr, error) {
	commandContext, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// argv is explicit operator configuration and is never passed to a shell.
	command := exec.CommandContext( // #nosec G204
		commandContext,
		s.argv[0],
		s.argv[1:]...,
	)
	command.Dir = "/"
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	command.Stdin = nil
	stdout := &limitedBuffer{limit: maxSourceOutput}
	stderr := &limitedBuffer{limit: maxSourceOutput}
	command.Stdout = stdout
	command.Stderr = stderr

	if err := command.Run(); err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return netip.Addr{}, errors.New("command source timed out")
		}
		if errors.Is(err, errOutputLimit) {
			return netip.Addr{}, errors.New("command source output is too large")
		}
		return netip.Addr{}, errors.New("command source exited unsuccessfully")
	}
	if stdout.exceeded || stderr.exceeded {
		return netip.Addr{}, errors.New("command source output is too large")
	}

	return ParseAddress(stdout.Bytes(), s.family, s.allowNonPublic)
}

var errOutputLimit = errors.New("output limit exceeded")

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return 0, errOutputLimit
	}
	if len(data) > remaining {
		_, _ = b.Buffer.Write(data[:remaining])
		b.exceeded = true
		return remaining, errOutputLimit
	}
	return b.Buffer.Write(data)
}

var _ io.Writer = (*limitedBuffer)(nil)
