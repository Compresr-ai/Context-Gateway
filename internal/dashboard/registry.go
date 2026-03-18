// Instance registry for discovering all running gateway instances.
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

// registryMu protects file operations across goroutines in the same process.
var registryMu sync.Mutex

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

	withFileLock(func() {
		path := registryFile()
		instances := readRegistryLocked(path)

		// Remove stale entry for this port (if any)
		filtered := make([]Instance, 0, len(instances))
		for _, i := range instances {
			if i.Port != port {
				filtered = append(filtered, i)
			}
		}
		filtered = append(filtered, inst)

		writeRegistryLocked(path, filtered)
	})
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
	withFileLock(func() {
		path := registryFile()
		instances := readRegistryLocked(path)

		filtered := make([]Instance, 0, len(instances))
		for _, i := range instances {
			if i.Port != port {
				filtered = append(filtered, i)
			}
		}

		writeRegistryLocked(path, filtered)
	})
	log.Debug().Int("port", port).Msg("dashboard: deregistered instance")
}

// ActiveCount returns the number of instances currently in the registry file.
// Does not health-check — use DiscoverInstances for live checks.
func ActiveCount() int {
	var instances []Instance
	withFileLock(func() {
		instances = readRegistryLocked(registryFile())
	})
	return len(instances)
}

// DiscoverInstances reads the registry and returns all live instances.
// It health-checks each one and removes dead entries.
func DiscoverInstances() []Instance {
	path := registryFile()

	// Read registry under lock
	var instances []Instance
	withFileLock(func() {
		instances = readRegistryLocked(path)
	})

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

	client := &http.Client{Timeout: 5 * time.Second}
	for _, inst := range instances {
		wg.Add(1)
		go func(i Instance) {
			defer wg.Done()
			url := fmt.Sprintf("http://localhost:%d/health", i.Port)
			resp, err := client.Get(url) // #nosec G107 -- localhost health check only
			alive := err == nil && resp != nil && resp.StatusCode == http.StatusOK
			if !alive {
				log.Debug().Int("port", i.Port).Err(err).Msg("dashboard: health check failed for instance")
			}
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
	var toRemove []int // ports of confirmed-dead instances to prune from registry
	for r := range results {
		if r.alive {
			live = append(live, r.inst)
			continue
		}
		// Health check failed. Only remove from registry if the process PID is
		// confirmed dead — this distinguishes crashed/stopped instances from
		// transient network hiccups on a still-running gateway.
		if !isPIDAlive(r.inst.PID) {
			log.Debug().Int("port", r.inst.Port).Int("pid", r.inst.PID).
				Msg("dashboard: removing dead instance from registry")
			toRemove = append(toRemove, r.inst.Port)
		}
	}

	if len(toRemove) > 0 {
		remove := make(map[int]bool, len(toRemove))
		for _, p := range toRemove {
			remove[p] = true
		}
		withFileLock(func() {
			all := readRegistryLocked(path)
			filtered := make([]Instance, 0, len(all))
			for _, i := range all {
				if !remove[i.Port] {
					filtered = append(filtered, i)
				}
			}
			writeRegistryLocked(path, filtered)
		})
	}

	return live
}

// isPIDAlive is defined in registry_unix.go (Unix) and registry_windows.go (Windows).

// readRegistryLocked reads the registry file. Must be called with file lock held.
func readRegistryLocked(path string) []Instance {
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
	found := false
	withFileLock(func() {
		path := registryFile()
		instances := readRegistryLocked(path)
		for i := range instances {
			if instances[i].Port == port {
				instances[i].AgentName = newName
				found = true
				break
			}
		}
		if found {
			writeRegistryLocked(path, instances)
		}
	})
	return found
}

// writeRegistryLocked writes the registry file. Must be called with file lock held.
func writeRegistryLocked(path string, instances []Instance) {
	// Ensure we never write null - always write an empty array at minimum
	if instances == nil {
		instances = []Instance{}
	}
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
