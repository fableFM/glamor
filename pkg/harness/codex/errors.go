package codex

import (
	"errors"
	"fmt"
)

var (
	// ErrConfig identifies invalid SDK or Codex CLI configuration.
	ErrConfig = errors.New("codex config error")
	// ErrOutputSchema identifies failures while preparing a turn output schema.
	ErrOutputSchema = errors.New("codex output schema error")
	// ErrDecodeEvent identifies invalid JSONL emitted by the Codex CLI.
	ErrDecodeEvent = errors.New("codex event decode error")
	// ErrThreadEvent identifies an error event emitted by the Codex CLI.
	ErrThreadEvent = errors.New("codex thread event error")
	// ErrTurnFailed identifies a failed Codex turn.
	ErrTurnFailed = errors.New("codex turn failed")
	// ErrExec identifies failures while running the Codex CLI process.
	ErrExec = errors.New("codex exec error")
)

// ConfigError describes an invalid config override before Codex is started.
type ConfigError struct {
	Path string
	Err  error
}

func (e *ConfigError) Error() string {
	if e == nil {
		return ErrConfig.Error()
	}
	if e.Path != "" {
		return fmt.Sprintf("%s at %s: %v", ErrConfig, e.Path, e.Err)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", ErrConfig, e.Err)
	}
	return ErrConfig.Error()
}

func (e *ConfigError) Unwrap() error {
	if e == nil {
		return ErrConfig
	}
	return errors.Join(ErrConfig, e.Err)
}

// OutputSchemaError describes a failure while creating the temporary schema file.
type OutputSchemaError struct {
	Op  string
	Err error
}

func (e *OutputSchemaError) Error() string {
	if e == nil {
		return ErrOutputSchema.Error()
	}
	if e.Op != "" {
		return fmt.Sprintf("%s during %s: %v", ErrOutputSchema, e.Op, e.Err)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", ErrOutputSchema, e.Err)
	}
	return ErrOutputSchema.Error()
}

func (e *OutputSchemaError) Unwrap() error {
	if e == nil {
		return ErrOutputSchema
	}
	return errors.Join(ErrOutputSchema, e.Err)
}

// DecodeEventError describes a JSONL event that could not be decoded.
type DecodeEventError struct {
	Line []byte
	Err  error
}

func (e *DecodeEventError) Error() string {
	if e == nil {
		return ErrDecodeEvent.Error()
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", ErrDecodeEvent, e.Err)
	}
	return ErrDecodeEvent.Error()
}

func (e *DecodeEventError) Unwrap() error {
	if e == nil {
		return ErrDecodeEvent
	}
	return errors.Join(ErrDecodeEvent, e.Err)
}

// ThreadEventError describes an error event emitted in the Codex JSONL stream.
type ThreadEventError struct {
	Message string
	Event   ThreadEvent
}

func (e *ThreadEventError) Error() string {
	if e == nil {
		return ErrThreadEvent.Error()
	}
	if e.Message != "" {
		return fmt.Sprintf("%s: %s", ErrThreadEvent, e.Message)
	}
	return ErrThreadEvent.Error()
}

func (e *ThreadEventError) Unwrap() error {
	return ErrThreadEvent
}

// TurnFailedError describes a turn.failed event emitted by Codex.
type TurnFailedError struct {
	ThreadError *ThreadError
}

func (e *TurnFailedError) Error() string {
	if e == nil {
		return ErrTurnFailed.Error()
	}
	if e.ThreadError != nil && e.ThreadError.Message != "" {
		return fmt.Sprintf("%s: %s", ErrTurnFailed, e.ThreadError.Message)
	}
	return ErrTurnFailed.Error()
}

func (e *TurnFailedError) Unwrap() error {
	if e == nil || e.ThreadError == nil {
		return ErrTurnFailed
	}
	return errors.Join(ErrTurnFailed, e.ThreadError)
}

// ExecError describes failures while preparing, running, or waiting for Codex.
type ExecError struct {
	Op     string
	Stderr string
	Err    error
}

func (e *ExecError) Error() string {
	if e == nil {
		return ErrExec.Error()
	}
	message := ErrExec.Error()
	if e.Op != "" {
		message += " during " + e.Op
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	if e.Stderr != "" {
		message += ": " + e.Stderr
	}
	return message
}

func (e *ExecError) Unwrap() error {
	if e == nil {
		return ErrExec
	}
	return errors.Join(ErrExec, e.Err)
}
