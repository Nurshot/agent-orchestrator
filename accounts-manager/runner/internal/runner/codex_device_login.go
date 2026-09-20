package runner

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
)

const (
	codexDeviceVerificationURL = "https://auth.openai.com/codex/device"
	codexDeviceCodePrefix      = "Codex device code:"
)

func runCodexDeviceLogin(ctx context.Context, stateDir string) error {
	state, err := LoadState(stateDir)
	if err != nil {
		return err
	}
	store := sdkauth.NewFileTokenStore()
	manager := sdkauth.NewManager(store, sdkauth.NewCodexAuthenticator())
	_, _, err = manager.Login(ctx, "codex", state.Config, &sdkauth.LoginOptions{
		NoBrowser: true,
		Metadata:  map[string]string{"codex_login_mode": "device"},
	})
	return err
}

func newCodexDeviceProcessStarter(stateDir string) func(context.Context) (codexDeviceLogin, error) {
	return func(ctx context.Context) (codexDeviceLogin, error) {
		executable, err := os.Executable()
		if err != nil {
			return codexDeviceLogin{}, errors.New("device login unavailable")
		}
		command := exec.CommandContext(ctx, executable, "_codex-device-login", "--state-dir", stateDir)
		stdout, err := command.StdoutPipe()
		if err != nil {
			return codexDeviceLogin{}, errors.New("device login unavailable")
		}
		command.Stderr = io.Discard
		if err = command.Start(); err != nil {
			return codexDeviceLogin{}, errors.New("device login unavailable")
		}

		code := make(chan string, 1)
		done := make(chan error, 1)
		var once sync.Once
		go func() {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if value := strings.TrimSpace(strings.TrimPrefix(line, codexDeviceCodePrefix)); value != line && value != "" {
					once.Do(func() { code <- value })
				}
			}
			errWait := command.Wait()
			if errWait == nil && scanner.Err() != nil {
				errWait = scanner.Err()
			}
			done <- errWait
			close(done)
		}()

		select {
		case userCode := <-code:
			return codexDeviceLogin{AuthorizationURL: codexDeviceVerificationURL, UserCode: userCode, Done: done}, nil
		case errWait := <-done:
			select {
			case userCode := <-code:
				return codexDeviceLogin{AuthorizationURL: codexDeviceVerificationURL, UserCode: userCode, Done: closedDeviceResult(errWait)}, nil
			default:
			}
			return codexDeviceLogin{}, errors.New("device login unavailable")
		case <-ctx.Done():
			return codexDeviceLogin{}, errors.New("device login unavailable")
		case <-time.After(30 * time.Second):
			return codexDeviceLogin{}, errors.New("device login unavailable")
		}
	}
}

func closedDeviceResult(err error) <-chan error {
	done := make(chan error, 1)
	done <- err
	close(done)
	return done
}
