package runner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	sdkapi "github.com/router-for-me/CLIProxyAPI/v7/sdk/api"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const leaseDuration = 45 * time.Second

// Serve starts the embedded upstream engine and blocks until AO's context is
// cancelled, the engine exits, or the daemon lease expires.
func Serve(ctx context.Context, stateDir string) error {
	state, err := LoadState(stateDir)
	if err != nil {
		return err
	}
	instanceID, err := NewInstanceID()
	if err != nil {
		return err
	}
	startedAt := time.Now().UTC()
	if err = WriteRuntimeRecord(state.Root, RuntimeRecord{
		PID:             os.Getpid(),
		Port:            state.Config.Port,
		InstanceID:      instanceID,
		RunnerVersion:   Version,
		UpstreamVersion: UpstreamVersion,
		StartedAt:       startedAt,
	}); err != nil {
		return err
	}

	lease := NewLease(leaseDuration)
	control := NewControlHandler(ControlIdentity{
		InstanceID:    instanceID,
		RunnerVersion: Version,
		EngineVersion: UpstreamVersion,
	}, state.ControlKey, lease)
	oauth := newOAuthCoordinator(
		fmt.Sprintf("http://127.0.0.1:%d", state.Config.Port),
		state.ManagementKey,
		&http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		nil,
	)
	oauth.startCodexDevice = newCodexDeviceProcessStarter(state.Root)
	defer oauth.Close()
	routeCapability, err := newRouteCapability(state.RoutingKey)
	if err != nil {
		return err
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", state.Config.Port)
	tokenStore := sdkauth.GetTokenStore()
	if dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) }); ok {
		dirSetter.SetBaseDir(state.Config.AuthDir)
	}
	coreManager := coreauth.NewManager(tokenStore, nil, nil)
	accessManager := sdkaccess.NewManager()
	sdkaccess.RegisterProvider(routeAccessProviderType, routeCapability)
	defer sdkaccess.UnregisterProvider(routeAccessProviderType)
	routeHandler := newRouteTokenHandler(state.ManagementKey, baseURL, routeCapability, func(provider, authIndex string) bool {
		for _, auth := range coreManager.List() {
			if auth != nil && auth.Index == authIndex && strings.EqualFold(auth.Provider, provider) && !auth.Disabled && !auth.Unavailable && auth.Status == coreauth.StatusActive {
				return true
			}
		}
		return false
	})

	previousPassword, passwordWasSet := os.LookupEnv("MANAGEMENT_PASSWORD")
	if err = os.Setenv("MANAGEMENT_PASSWORD", state.ManagementKey); err != nil {
		return fmt.Errorf("configure private management access: %w", err)
	}
	defer func() {
		if passwordWasSet {
			_ = os.Setenv("MANAGEMENT_PASSWORD", previousPassword)
		} else {
			_ = os.Unsetenv("MANAGEMENT_PASSWORD")
		}
	}()

	service, err := cliproxy.NewBuilder().
		WithConfig(state.Config).
		WithConfigPath(state.ConfigPath).
		WithRequestAccessManager(accessManager).
		WithCoreAuthManager(coreManager).
		WithLocalManagementPassword(state.ManagementKey).
		WithHooks(cliproxy.Hooks{OnAfterStart: func(*cliproxy.Service) {
			coreManager.SetSelector(newExactRouteSelector(routeCapability, coreManager.Selector()))
		}}).
		WithServerOptions(sdkapi.WithRouterConfigurator(func(router *gin.Engine, _ *handlers.BaseAPIHandler, _ *sdkconfig.Config) {
			router.GET("/ao/internal/identity", gin.WrapH(control))
			router.POST("/ao/internal/lease", gin.WrapH(control))
			router.POST("/ao/internal/oauth/start", gin.WrapH(oauth))
			router.GET("/ao/internal/oauth/status", gin.WrapH(oauth))
			router.GET("/ao/internal/oauth/events", gin.WrapH(oauth))
			router.DELETE("/ao/internal/oauth/session", gin.WrapH(oauth))
			router.POST("/ao/internal/routes/token", gin.WrapH(routeHandler))
		})).
		Build()
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go cancelWhenLeaseExpires(runCtx, cancel, lease)
	err = service.Run(runCtx)
	if errors.Is(err, context.Canceled) || runCtx.Err() != nil {
		return nil
	}
	return err
}

func cancelWhenLeaseExpires(ctx context.Context, cancel context.CancelFunc, lease *Lease) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if lease.Expired(now) {
				cancel()
				return
			}
		}
	}
}

var _ http.Handler = (*controlHandler)(nil)
