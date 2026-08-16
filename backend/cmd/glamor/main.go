// Command glamor — CLI-клиент демона glamord (D-01, T-12): тонкий клиент
// REST поверх daemon.json + токена.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fableFM/glamor/internal/daemon"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

const usage = `glamor — CLI демона glamord

Команды:
  status            состояние демона (healthz + version)
  runs              список ранов
  stop <run-id>     остановить ран
`

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	client, baseURL, err := newClient()
	if err != nil {
		return err
	}

	switch args[0] {
	case "status":
		return cmdStatus(client, baseURL)
	case "runs":
		return cmdRuns(client, baseURL)
	case "stop":
		if len(args) < 2 {
			return fmt.Errorf("usage: glamor stop <run-id>")
		}
		return cmdStop(client, baseURL, args[1])
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// newClient собирает HTTP-клиента из ~/.glamor/daemon.json + ~/.glamor/token.
func newClient() (*http.Client, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get home dir: %w", err)
	}
	glamorHome := filepath.Join(home, ".glamor")

	info, err := daemon.ReadInfo(filepath.Join(glamorHome, "daemon.json"))
	if err != nil {
		return nil, "", fmt.Errorf("daemon is not running? (%v)", err)
	}

	token, err := daemon.LoadOrCreateToken(filepath.Join(glamorHome, "token"))
	if err != nil {
		return nil, "", err
	}

	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &authTransport{token: token, base: http.DefaultTransport},
	}, info.URL, nil
}

type authTransport struct {
	token string
	base  http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

func get(client *http.Client, url string, out any) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: status %d: %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func cmdStatus(client *http.Client, baseURL string) error {
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		return fmt.Errorf("daemon unreachable: %w", err)
	}
	_ = resp.Body.Close()

	var version struct {
		Version   string   `json:"version"`
		Harnesses []string `json:"harnesses"`
	}
	if err := get(client, baseURL+"/version", &version); err != nil {
		return err
	}
	fmt.Printf("glamord %s at %s\nharnesses: %s\n",
		version.Version, baseURL, strings.Join(version.Harnesses, ", "))
	return nil
}

func cmdRuns(client *http.Client, baseURL string) error {
	var runs []struct {
		ID         string     `json:"id"`
		State      string     `json:"state"`
		TaskText   string     `json:"task_text"`
		Branch     string     `json:"branch"`
		CreatedAt  time.Time  `json:"created_at"`
		FinishedAt *time.Time `json:"finished_at"`
	}
	if err := get(client, baseURL+"/runs?limit=20", &runs); err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Println("no runs")
		return nil
	}
	for _, r := range runs {
		fmt.Printf("%s  %-12s  %s  %s\n", r.ID[:8], r.State, r.Branch, truncate(r.TaskText, 60))
	}
	return nil
}

func cmdStop(client *http.Client, baseURL, runID string) error {
	req, err := http.NewRequest(http.MethodPost, baseURL+"/runs/"+runID+"/stop", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST /runs/%s/stop: %w", runID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("stop failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	fmt.Println("run stopped:", runID)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
