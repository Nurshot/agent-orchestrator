package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const (
	configFileName = "config.yaml"
	controlKeyName = "control.key"
	authDirName    = "auth"
)

// State is the validated private runtime configuration owned by AO.
type State struct {
	Root       string
	ConfigPath string
	ControlKey string
	Config     *sdkconfig.Config
}

// LoadState loads the AO-owned runner state without accepting paths outside the
// requested state root or files that can be redirected through symlinks.
func LoadState(root string) (*State, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("state directory must be absolute")
	}
	root = filepath.Clean(root)
	if err := requirePrivateDirectory(root); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}

	authDir := filepath.Join(root, authDirName)
	if err := requirePrivateDirectory(authDir); err != nil {
		return nil, fmt.Errorf("auth directory: %w", err)
	}

	controlPath := filepath.Join(root, controlKeyName)
	controlBytes, err := readPrivateRegularFile(controlPath)
	if err != nil {
		return nil, fmt.Errorf("control key: %w", err)
	}
	controlKey := strings.TrimSpace(string(controlBytes))
	if controlKey == "" {
		return nil, fmt.Errorf("control key is empty")
	}

	configPath := filepath.Join(root, configFileName)
	if _, err = readPrivateRegularFile(configPath); err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	cfg, err := sdkconfig.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	if err = validateConfig(cfg, authDir); err != nil {
		return nil, err
	}

	return &State{
		Root:       root,
		ConfigPath: configPath,
		ControlKey: controlKey,
		Config:     cfg,
	}, nil
}

func validateConfig(cfg *sdkconfig.Config, authDir string) error {
	if cfg == nil {
		return fmt.Errorf("configuration is empty")
	}
	if cfg.Host != "127.0.0.1" {
		return fmt.Errorf("configuration must use the IPv4 loopback host")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("configuration port is invalid")
	}
	if filepath.Clean(cfg.AuthDir) != filepath.Clean(authDir) {
		return fmt.Errorf("configuration auth directory must be inside the state directory")
	}
	if cfg.TLS.Enable {
		return fmt.Errorf("configuration must not enable TLS")
	}
	if cfg.RemoteManagement.AllowRemote || strings.TrimSpace(cfg.RemoteManagement.SecretKey) != "" || !cfg.RemoteManagement.DisableControlPanel || !cfg.RemoteManagement.DisableAutoUpdatePanel {
		return fmt.Errorf("configuration must keep remote management disabled")
	}
	if cfg.Plugins.Enabled {
		return fmt.Errorf("configuration must keep plugins disabled")
	}
	if cfg.Pprof.Enable {
		return fmt.Errorf("configuration must keep pprof disabled")
	}
	if cfg.Discovery.Enabled {
		return fmt.Errorf("configuration must keep discovery disabled")
	}
	if len(cfg.APIKeys) != 1 || strings.TrimSpace(cfg.APIKeys[0]) == "" {
		return fmt.Errorf("configuration must contain exactly one internal client key")
	}
	return nil
}

func requirePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("must be a regular directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("permissions must not grant group or other access")
	}
	return nil
}

func readPrivateRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("permissions must not grant group or other access")
	}
	return os.ReadFile(path)
}
