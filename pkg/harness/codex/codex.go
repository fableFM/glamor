package codex

// Codex is the main client for interacting with the Codex agent.
type Codex struct {
	exec    *Exec
	options Options
}

// New creates a Codex client.
func New(options Options) (*Codex, error) {
	execRunner, err := NewExec(options)
	if err != nil {
		return nil, err
	}

	return &Codex{
		exec:    execRunner,
		options: options,
	}, nil
}

// MustNew creates a Codex client and panics if the codex executable cannot be found.
func MustNew(options Options) *Codex {
	client, err := New(options)
	if err != nil {
		panic(err)
	}
	return client
}

// StartThread starts a new conversation with an agent.
func (c *Codex) StartThread(options ThreadOptions) *Thread {
	return newThread(c.exec, c.options, options, "")
}

// ResumeThread resumes a persisted Codex conversation by thread ID.
func (c *Codex) ResumeThread(id string, options ThreadOptions) *Thread {
	return newThread(c.exec, c.options, options, id)
}
