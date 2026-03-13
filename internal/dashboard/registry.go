// Instance registry for discovering all running gateway instances.
//
// DESIGN: Each gateway registers itself in a shared JSON file on startup
// and deregisters on shutdown. The central dashboard reads this file to
// discover all instances and poll their /monitor/api/sessions endpoints.
//
// File location: ~/.config/context-gateway/instances.json
package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Instance represents a running gateway process.
type Instance struct {
	PID         int       `json:"pid"`
	Port        int       `json:"port"`
	AgentName   string    `json:"agent_name"`
	SessionDir  string    `json:"session_dir"`
	StartedAt   time.Time `json:"started_at"`
	TermProgram string    `json:"term_program"` // e.g. "iTerm.app", "Apple_Terminal", "WarpTerminal"
	TTY         string    `json:"tty"`          // e.g. "/dev/ttys003"
}

// registryFile returns the path to the shared instances registry.
func registryFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/context-gateway-instances.json"
	}
	dir := filepath.Join(home, ".config", "context-gateway")
	_ = os.MkdirAll(dir, 0750)
	return filepath.Join(dir, "instances.json")
}

// Register adds this gateway instance to the shared registry.
func Register(port int, agentName, sessionDir string) {
	inst := Instance{
		PID:         os.Getpid(),
		Port:        port,
		AgentName:   agentName,
		SessionDir:  sessionDir,
		StartedAt:   time.Now(),
		TermProgram: os.Getenv("TERM_PROGRAM"),
		TTY:         detectTTY(),
	}

	path := registryFile()
	instances := readRegistry(path)

	// Remove stale entry for this port (if any)
	filtered := make([]Instance, 0, len(instances))
	for _, i := range instances {
		if i.Port != port {
			filtered = append(filtered, i)
		}
	}
	filtered = append(filtered, inst)

	writeRegistry(path, filtered)
	log.Debug().Int("port", port).Str("term", inst.TermProgram).Str("tty", inst.TTY).Msg("dashboard: registered instance")
}

// detectTTY returns the TTY device for the current process (e.g. "/dev/ttys003").
func detectTTY() string {
	if runtime.GOOS == "darwin" {
		// On macOS, os.Stdin.Name() returns "/dev/stdin" which is useless.
		// Shell out to `tty` which returns the actual device like "/dev/ttys003".
		out, err := exec.Command("tty").Output() // #nosec G204 -- fixed command
		if err == nil {
			if tty := strings.TrimSpace(string(out)); tty != "" && tty != "not a tty" {
				return tty
			}
		}
		return ""
	}
	// Linux: read the symlink for stdin
	ttyPath := fmt.Sprintf("/proc/%d/fd/0", os.Getpid())
	if target, err := os.Readlink(ttyPath); err == nil {
		return target
	}
	return ""
}

// Deregister removes this gateway instance from the shared registry.
func Deregister(port int) {
	path := registryFile()
	instances := readRegistry(path)

	filtered := make([]Instance, 0, len(instances))
	for _, i := range instances {
		if i.Port != port {
			filtered = append(filtered, i)
		}
	}

	writeRegistry(path, filtered)
	log.Debug().Int("port", port).Msg("dashboard: deregistered instance")
}

// DiscoverInstances reads the registry and returns all live instances.
// It health-checks each one and removes dead entries.
func DiscoverInstances() []Instance {
	path := registryFile()
	instances := readRegistry(path)

	if len(instances) == 0 {
		return nil
	}

	// Health-check all instances in parallel
	type result struct {
		inst  Instance
		alive bool
	}
	results := make(chan result, len(instances))
	var wg sync.WaitGroup

	client := &http.Client{Timeout: 2 * time.Second}
	for _, inst := range instances {
		wg.Add(1)
		go func(i Instance) {
			defer wg.Done()
			url := fmt.Sprintf("http://localhost:%d/health", i.Port)
			resp, err := client.Get(url) // #nosec G107 -- localhost health check only
			alive := err == nil && resp != nil && resp.StatusCode == http.StatusOK
			if resp != nil {
				_ = resp.Body.Close()
			}
			results <- result{inst: i, alive: alive}
		}(inst)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var live []Instance
	changed := false
	for r := range results {
		if r.alive {
			live = append(live, r.inst)
		} else {
			changed = true
		}
	}

	// Clean up dead entries
	if changed {
		writeRegistry(path, live)
	}

	return live
}

func readRegistry(path string) []Instance {
	data, err := os.ReadFile(path) // #nosec G304 -- fixed config path
	if err != nil {
		return nil
	}
	var instances []Instance
	if err := json.Unmarshal(data, &instances); err != nil {
		return nil
	}
	return instances
}

// RenameInstance updates the AgentName for the instance on the given port.
func RenameInstance(port int, newName string) bool {
	path := registryFile()
	instances := readRegistry(path)
	found := false
	for i := range instances {
		if instances[i].Port == port {
			instances[i].AgentName = newName
			found = true
			break
		}
	}
	if found {
		writeRegistry(path, instances)
	}
	return found
}

func writeRegistry(path string, instances []Instance) {
	data, err := json.MarshalIndent(instances, "", "  ")
	if err != nil {
		return
	}
	// Atomic write via temp file + rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
