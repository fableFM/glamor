package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const (
	internalOriginatorEnv = "CODEX_INTERNAL_ORIGINATOR_OVERRIDE"
	goSDKOriginator       = "codex_sdk_go"
)

// ExecArgs are translated into codex exec CLI arguments.
type ExecArgs struct {
	Input string

	BaseURL  string
	APIKey   string
	ThreadID string
	Images   []string

	Model                string
	SandboxMode          SandboxMode
	WorkingDirectory     string
	AdditionalDirs       []string
	SkipGitRepoCheck     bool
	OutputSchemaFile     string
	ModelReasoningEffort ModelReasoningEffort
	NetworkAccessEnabled *bool
	WebSearchMode        WebSearchMode
	WebSearchEnabled     *bool
	ApprovalPolicy       ApprovalMode
}

// Exec runs the Codex CLI and streams stdout JSONL lines.
type Exec struct {
	path   string
	env    map[string]string
	config ConfigObject
}

// NewExec creates a Codex CLI runner.
func NewExec(options Options) (*Exec, error) {
	path := options.CodexPath
	if path == "" {
		resolved, err := exec.LookPath("codex")
		if err != nil {
			return nil, &ExecError{Op: "find executable", Err: err}
		}
		path = resolved
	}

	return &Exec{
		path:   path,
		env:    copyStringMap(options.Env),
		config: copyConfigObject(options.Config),
	}, nil
}

// Run executes codex exec and calls onLine for every stdout JSONL line.
func (e *Exec) Run(ctx context.Context, args ExecArgs, onLine func([]byte) error) error {
	commandArgs, err := e.commandArgs(args)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, e.path, commandArgs...)
	cmd.Env = e.buildEnv(args)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return &ExecError{Op: "open stdin", Err: err}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &ExecError{Op: "open stdout", Err: err}
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return &ExecError{Op: "start", Err: err}
	}

	if _, err := stdin.Write([]byte(args.Input)); err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return &ExecError{Op: "write stdin", Err: err}
	}
	if err := stdin.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return &ExecError{Op: "close stdin", Err: err}
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024), 10*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if err := onLine(line); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return &ExecError{Op: "read stdout", Err: err}
	}

	if err := cmd.Wait(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = errors.Join(ctxErr, err)
		}
		return &ExecError{Op: "exit", Stderr: detail, Err: err}
	}

	return nil
}

func (e *Exec) commandArgs(args ExecArgs) ([]string, error) {
	commandArgs := []string{"exec", "--experimental-json"}

	configOverrides, err := serializeConfigOverrides(e.config)
	if err != nil {
		return nil, err
	}
	for _, override := range configOverrides {
		commandArgs = append(commandArgs, "--config", override)
	}

	if args.BaseURL != "" {
		value, err := toTOMLValue(args.BaseURL, "openai_base_url")
		if err != nil {
			return nil, err
		}
		commandArgs = append(commandArgs, "--config", "openai_base_url="+value)
	}
	if args.Model != "" {
		commandArgs = append(commandArgs, "--model", args.Model)
	}
	if args.SandboxMode != "" {
		commandArgs = append(commandArgs, "--sandbox", string(args.SandboxMode))
	}
	if args.WorkingDirectory != "" {
		commandArgs = append(commandArgs, "--cd", args.WorkingDirectory)
	}
	for _, dir := range args.AdditionalDirs {
		commandArgs = append(commandArgs, "--add-dir", dir)
	}
	if args.SkipGitRepoCheck {
		commandArgs = append(commandArgs, "--skip-git-repo-check")
	}
	if args.OutputSchemaFile != "" {
		commandArgs = append(commandArgs, "--output-schema", args.OutputSchemaFile)
	}
	if args.ModelReasoningEffort != "" {
		commandArgs = append(
			commandArgs,
			"--config",
			fmt.Sprintf("model_reasoning_effort=%q", args.ModelReasoningEffort),
		)
	}
	if args.NetworkAccessEnabled != nil {
		commandArgs = append(
			commandArgs,
			"--config",
			fmt.Sprintf("sandbox_workspace_write.network_access=%t", *args.NetworkAccessEnabled),
		)
	}
	if args.WebSearchMode != "" {
		commandArgs = append(commandArgs, "--config", fmt.Sprintf("web_search=%q", args.WebSearchMode))
	} else if args.WebSearchEnabled != nil {
		mode := WebSearchModeDisabled
		if *args.WebSearchEnabled {
			mode = WebSearchModeLive
		}
		commandArgs = append(commandArgs, "--config", fmt.Sprintf("web_search=%q", mode))
	}
	if args.ApprovalPolicy != "" {
		commandArgs = append(commandArgs, "--config", fmt.Sprintf("approval_policy=%q", args.ApprovalPolicy))
	}
	if args.ThreadID != "" {
		commandArgs = append(commandArgs, "resume", args.ThreadID)
	}
	for _, image := range args.Images {
		commandArgs = append(commandArgs, "--image", image)
	}

	return commandArgs, nil
}

func (e *Exec) buildEnv(args ExecArgs) []string {
	env := map[string]string{}
	if e.env != nil {
		for key, value := range e.env {
			env[key] = value
		}
	} else {
		for _, pair := range os.Environ() {
			key, value, ok := strings.Cut(pair, "=")
			if ok {
				env[key] = value
			}
		}
	}

	if _, ok := env[internalOriginatorEnv]; !ok {
		env[internalOriginatorEnv] = goSDKOriginator
	}
	if args.APIKey != "" {
		env["CODEX_API_KEY"] = args.APIKey
	}

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

func serializeConfigOverrides(config ConfigObject) ([]string, error) {
	if len(config) == 0 {
		return []string{}, nil
	}

	overrides := []string{}
	keys := sortedKeys(config)
	for _, key := range keys {
		if key == "" {
			return nil, &ConfigError{Err: errors.New("override keys must be non-empty strings")}
		}
		if err := flattenConfigOverride(config[key], key, &overrides); err != nil {
			return nil, err
		}
	}
	return overrides, nil
}

func flattenConfigOverride(value ConfigValue, prefix string, overrides *[]string) error {
	if nested, ok := value.(ConfigObject); ok {
		keys := sortedKeys(nested)
		if len(keys) == 0 {
			*overrides = append(*overrides, prefix+"={}")
			return nil
		}
		for _, key := range keys {
			if key == "" {
				return &ConfigError{
					Path: prefix,
					Err:  errors.New("override keys must be non-empty strings"),
				}
			}
			if err := flattenConfigOverride(nested[key], prefix+"."+key, overrides); err != nil {
				return err
			}
		}
		return nil
	}

	if nested, ok := value.(map[string]any); ok {
		return flattenConfigOverride(configObjectFromAny(nested), prefix, overrides)
	}

	rendered, err := toTOMLValue(value, prefix)
	if err != nil {
		return err
	}
	*overrides = append(*overrides, prefix+"="+rendered)
	return nil
}

func toTOMLValue(value ConfigValue, path string) (string, error) {
	switch v := value.(type) {
	case string:
		return strconv.Quote(v), nil
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int8:
		return strconv.FormatInt(int64(v), 10), nil
	case int16:
		return strconv.FormatInt(int64(v), 10), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case []string:
		items := make([]string, 0, len(v))
		for index, item := range v {
			rendered, err := toTOMLValue(item, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return "", err
			}
			items = append(items, rendered)
		}
		return "[" + strings.Join(items, ", ") + "]", nil
	case []any:
		items := make([]string, 0, len(v))
		for index, item := range v {
			rendered, err := toTOMLValue(item, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return "", err
			}
			items = append(items, rendered)
		}
		return "[" + strings.Join(items, ", ") + "]", nil
	case ConfigObject:
		return inlineTOMLObject(v, path)
	case map[string]any:
		return inlineTOMLObject(configObjectFromAny(v), path)
	case json.RawMessage:
		return string(v), nil
	case nil:
		return "", &ConfigError{Path: path, Err: errors.New("value cannot be nil")}
	default:
		return "", &ConfigError{Path: path, Err: fmt.Errorf("unsupported value type %T", value)}
	}
}

func inlineTOMLObject(object ConfigObject, path string) (string, error) {
	parts := []string{}
	for _, key := range sortedKeys(object) {
		rendered, err := toTOMLValue(object[key], path+"."+key)
		if err != nil {
			return "", err
		}
		parts = append(parts, formatTOMLKey(key)+" = "+rendered)
	}
	return "{" + strings.Join(parts, ", ") + "}", nil
}

func formatTOMLKey(key string) string {
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return strconv.Quote(key)
	}
	if key == "" {
		return strconv.Quote(key)
	}
	return key
}

func sortedKeys(m map[string]ConfigValue) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func copyStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func copyConfigObject(source ConfigObject) ConfigObject {
	if source == nil {
		return nil
	}
	result := make(ConfigObject, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func configObjectFromAny(source map[string]any) ConfigObject {
	result := make(ConfigObject, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
