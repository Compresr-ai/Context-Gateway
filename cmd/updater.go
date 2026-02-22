// Package main provides update and uninstall functionality for context-gateway.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// Version is set at build time via ldflags
	Version = "v0.1.0"

	// GitHub repo for updates
	DefaultRepo = "Compresr-ai/Context-Gateway"

	// Colors
	colorGreen  = "\033[38;2;23;128;68m"
	colorYellow = "\033[1;33m"
	colorCyan   = "\033[0;36m"
	colorRed    = "\033[0;31m"
	colorBold   = "\033[1m"
	colorReset  = "\033[0m"
)

// GitHubRelease represents a GitHub release response
type GitHubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}

// getRepo returns the repo to use (can be overridden via env)
func getRepo() string {
	if repo := os.Getenv("CONTEXT_GATEWAY_REPO"); repo != "" {
		return repo
	}
	return DefaultRepo
}

// getConfigDir returns ~/.config/context-gateway
func getConfigDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".config", "context-gateway")
}

// getVersionFile returns path to version file
func getVersionFile() string {
	return filepath.Join(getConfigDir(), ".version")
}

// getCurrentVersion returns the current installed version
func getCurrentVersion() string {
	return Version
}

// getLatestVersion fetches the latest version from GitHub
func getLatestVersion() (string, error) {
	repo := getRepo()
	releasesURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	if _, err := url.ParseRequestURI(releasesURL); err != nil {
		return "", fmt.Errorf("invalid releases URL: %w", err)
	}

	resp, err := newHTTPClient().Get(releasesURL) // #nosec G107 -- URL host is fixed to api.github.com
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}

	return release.TagName, nil
}

// CheckForUpdates checks if a newer version is available
// Called on gateway startup
func CheckForUpdates() {
	current := getCurrentVersion()

	latest, err := getLatestVersion()
	if err != nil {
		// Silently fail - don't interrupt startup
		return
	}

	if current == latest {
		return
	}

	// Show update notification
	fmt.Printf("\n")
	fmt.Printf("%s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", colorYellow, colorBold, colorReset)
	fmt.Printf("%s%s  🔄 UPDATE AVAILABLE: %s → %s%s\n", colorYellow, colorBold, current, latest, colorReset)
	fmt.Printf("%s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", colorYellow, colorBold, colorReset)
	fmt.Printf("\n")
	fmt.Printf("  Run: %scontext-gateway update%s\n", colorCyan, colorReset)
	fmt.Printf("\n")
}

// DoUpdate downloads and installs the latest version
func DoUpdate() error {
	current := getCurrentVersion()

	fmt.Printf("\n%s%s  Checking for updates...%s\n\n", colorGreen, colorBold, colorReset)

	latest, err := getLatestVersion()
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	if current == latest {
		fmt.Printf("%s[✓]%s Already on latest version: %s\n", colorGreen, colorReset, current)
		return nil
	}

	fmt.Printf("  Updating: %s%s%s → %s%s%s\n\n", colorYellow, current, colorReset, colorGreen, latest, colorReset)

	// Stop any running gateway processes before replacing the binary
	// This prevents "zsh: killed" errors on macOS when replacing a running executable
	stopRunningGateways()

	// Get binary path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}

	// Construct download URL
	osName := runtime.GOOS
	arch := runtime.GOARCH
	filename := fmt.Sprintf("gateway-%s-%s", osName, arch)
	if osName == "windows" {
		filename += ".exe"
	}

	repo := getRepo()
	downloadURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, latest, filename)

	fmt.Printf("  Downloading from: %s\n", downloadURL)

	// Download new binary

	resp, err := newHTTPClient().Get(downloadURL) // #nosec G107 -- URL host is fixed to github.com
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	// Write to a secure temporary file in the target directory (same filesystem for atomic rename).
	out, err := os.CreateTemp(filepath.Dir(execPath), filepath.Base(execPath)+".new.*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpFile := out.Name()

	_, err = io.Copy(out, resp.Body)
	closeErr := out.Close()
	if err != nil {
		_ = os.Remove(tmpFile) // #nosec G703 -- tmpFile is generated by os.CreateTemp in target directory
		return fmt.Errorf("failed to write binary: %w", err)
	}
	if closeErr != nil {
		_ = os.Remove(tmpFile) // #nosec G703 -- tmpFile is generated by os.CreateTemp in target directory
		return fmt.Errorf("failed to finalize temp file: %w", closeErr)
	}

	// Make executable

	// #nosec G302,G703 -- installed binary must be executable by user; tmpFile comes from os.CreateTemp
	if err := os.Chmod(tmpFile, 0755); err != nil {
		// #nosec G703 -- tmpFile comes from os.CreateTemp
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to chmod: %w", err)
	}

	// Replace old binary
	oldFile := execPath + ".old"
	_ = os.Remove(oldFile)

	if err := os.Rename(execPath, oldFile); err != nil {
		// #nosec G703 -- tmpFile comes from os.CreateTemp
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to backup old binary: %w", err)
	}

	// #nosec G703 -- tmpFile comes from os.CreateTemp in the executable directory
	if err := os.Rename(tmpFile, execPath); err != nil {
		// Try to restore old binary
		if restoreErr := os.Rename(oldFile, execPath); restoreErr != nil {
			return fmt.Errorf("failed to install new binary: %w (failed to restore old binary: %v)", err, restoreErr)
		}
		return fmt.Errorf("failed to install new binary: %w", err)
	}

	// Remove old binary
	_ = os.Remove(oldFile)

	// Print success
	fmt.Printf("\n")
	fmt.Printf("%s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", colorGreen, colorBold, colorReset)
	fmt.Printf("%s%s  ✅ UPDATE COMPLETE!%s\n", colorGreen, colorBold, colorReset)
	fmt.Printf("%s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", colorGreen, colorBold, colorReset)
	fmt.Printf("\n")
	fmt.Printf("  Version: %s%s%s\n", colorGreen, latest, colorReset)
	fmt.Printf("\n")
	fmt.Printf("  Run: %scontext-gateway%s to start\n", colorCyan, colorReset)
	fmt.Printf("\n")

	return nil
}

// DoUninstall removes the gateway binary and optionally configs
func DoUninstall() error {
	fmt.Printf("\n%s%s⚠️  UNINSTALL CONTEXT-GATEWAY%s\n\n", colorYellow, colorBold, colorReset)

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}

	installDir := filepath.Dir(execPath)
	configDir := getConfigDir()

	fmt.Printf("This will remove:\n")
	fmt.Printf("  • %s%s%s\n", colorCyan, execPath, colorReset)
	fmt.Printf("  • %s%s/compresr%s (symlink)\n", colorCyan, installDir, colorReset)
	fmt.Printf("\n")
	fmt.Printf("Config files will be %spreserved%s at: %s%s%s\n", colorBold, colorReset, colorCyan, configDir, colorReset)
	fmt.Printf("\n")
	fmt.Printf("Continue? [y/N] ")

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))

	if input != "y" && input != "yes" {
		fmt.Printf("\nUninstall cancelled.\n")
		return nil
	}

	// Remove symlink
	compresr := filepath.Join(installDir, "compresr")
	if _, err := os.Lstat(compresr); err == nil {
		_ = os.Remove(compresr)
		fmt.Printf("%s[✓]%s Removed %s\n", colorGreen, colorReset, compresr)
	}

	// Remove version file
	_ = os.Remove(getVersionFile())

	// Remove binary (self-delete)
	// On Unix, we can delete ourselves while running
	if err := os.Remove(execPath); err != nil {
		return fmt.Errorf("failed to remove binary: %w", err)
	}
	fmt.Printf("%s[✓]%s Removed %s\n", colorGreen, colorReset, execPath)

	// Print success
	fmt.Printf("\n")
	fmt.Printf("%s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", colorGreen, colorBold, colorReset)
	fmt.Printf("%s%s  ✅ UNINSTALL COMPLETE%s\n", colorGreen, colorBold, colorReset)
	fmt.Printf("%s%s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n", colorGreen, colorBold, colorReset)
	fmt.Printf("\n")
	fmt.Printf("  Config files preserved at: %s%s%s\n", colorCyan, configDir, colorReset)
	fmt.Printf("\n")
	fmt.Printf("  To remove configs too:\n")
	fmt.Printf("    %srm -rf %s%s\n", colorCyan, configDir, colorReset)
	fmt.Printf("\n")
	fmt.Printf("  To reinstall:\n")
	fmt.Printf("    %scurl -fsSL https://compresr.ai/install_gateway.sh | sh%s\n", colorCyan, colorReset)
	fmt.Printf("\n")

	return nil
}

// PrintVersion prints the current version
func PrintVersion() {
	printBanner()
	fmt.Printf("context-gateway %s\n", Version)
	fmt.Printf("Runtime: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

// stopRunningGateways stops any running context-gateway processes to allow binary replacement.
// This prevents "zsh: killed" errors on macOS when replacing a running executable.
func stopRunningGateways() {
	if runtime.GOOS == "windows" {
		// Windows can replace running executables
		return
	}

	// Find gateway processes
	// #nosec G204 -- fixed command, no user input
	cmd := exec.Command("pgrep", "-f", "context-gateway")
	output, err := cmd.Output()
	if err != nil {
		// No matching processes found
		return
	}

	pids := strings.Fields(strings.TrimSpace(string(output)))
	if len(pids) == 0 {
		return
	}

	currentPID := fmt.Sprintf("%d", os.Getpid())
	var stoppedAny bool

	for _, pid := range pids {
		// Don't kill ourselves
		if pid == currentPID {
			continue
		}

		fmt.Printf("  Stopping running gateway (PID: %s)...\n", pid)
		// #nosec G204 -- pid is from pgrep output, not user input
		killCmd := exec.Command("kill", "-TERM", pid)
		if killErr := killCmd.Run(); killErr == nil {
			stoppedAny = true
		}
	}

	if stoppedAny {
		// Wait for processes to exit gracefully
		time.Sleep(2 * time.Second)
	}
}
