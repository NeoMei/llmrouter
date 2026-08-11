package onboarding

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
)

type ClientAdapter interface {
	ID() ClientID
	Discover(Platform, string) Discovery
	Plan(context.Context, ConfigureRequest) (ConfigPlan, error)
	Check(context.Context, CheckRequest) []Check
}

type Platform struct{ OS, Arch string }

type Discovery struct {
	Installed bool
	Paths     []string
	Automatic bool
	Reason    string
}

type ConfigureRequest struct {
	Home         string
	Platform     Platform
	ServerURL    *url.URL
	Token        Secret
	Model        string
	BridgePath   string
	BridgeSHA256 string
}

type PlannedWrite struct {
	Path string
	Data []byte
	Mode fs.FileMode
}

type ConfigPlan struct {
	Client          ClientID
	Writes          []PlannedWrite
	Instructions    []string
	RestartRequired bool
}

type CheckRequest struct {
	Home      string
	Platform  Platform
	ServerURL *url.URL
	Token     Secret
	Model     string
}

type ConfigureResult struct {
	Receipt         Receipt
	Instructions    []string
	RestartRequired bool
}

var adapters = map[ClientID]ClientAdapter{
	ClientCodexCLI:      codexAdapter{id: ClientCodexCLI},
	ClientCodexDesktop:  codexAdapter{id: ClientCodexDesktop},
	ClientClaudeCode:    claudeCodeAdapter{},
	ClientClaudeDesktop: claudeDesktopAdapter{},
	ClientOpenCode:      openCodeAdapter{},
	ClientContinue:      continueAdapter{},
	ClientAider:         aiderAdapter{},
}

func AdapterFor(id ClientID) (ClientAdapter, bool) {
	adapter, ok := adapters[id]
	return adapter, ok
}

func RegisteredAdapters() []ClientAdapter {
	ids := make([]string, 0, len(adapters))
	for id := range adapters {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	result := make([]ClientAdapter, 0, len(ids))
	for _, id := range ids {
		result = append(result, adapters[ClientID(id)])
	}
	return result
}

// ConfigureClients plans every selected adapter before allowlisting or staging
// any destination. The transaction then allowlists the complete exact path set
// before the first Write, so planning/allowlist failures cannot partially apply.
func ConfigureClients(ctx context.Context, request ConfigureRequest, clients []ClientID) (ConfigureResult, error) {
	if len(clients) == 0 {
		return ConfigureResult{}, fmt.Errorf("%w: at least one client is required", ErrUsage)
	}
	if err := validateConfigureRequest(request); err != nil {
		return ConfigureResult{}, err
	}
	plans := make([]ConfigPlan, 0, len(clients))
	for _, id := range clients {
		adapter, ok := AdapterFor(id)
		if !ok {
			return ConfigureResult{}, fmt.Errorf("%w: adapter for %s is not registered", ErrUnavailable, id)
		}
		plan, err := adapter.Plan(ctx, request)
		if err != nil {
			return ConfigureResult{}, err
		}
		plans = append(plans, plan)
	}

	writes := make(map[string]PlannedWrite)
	var instructions []string
	restart := false
	for _, plan := range plans {
		instructions = append(instructions, plan.Instructions...)
		restart = restart || plan.RestartRequired
		for _, write := range plan.Writes {
			path, err := filepath.Abs(write.Path)
			if err != nil {
				return ConfigureResult{}, fmt.Errorf("%w: resolve planned destination", ErrUnsafe)
			}
			write.Path = filepath.Clean(path)
			if previous, exists := writes[write.Path]; exists {
				if string(previous.Data) != string(write.Data) || previous.Mode != write.Mode {
					return ConfigureResult{}, fmt.Errorf("%w: adapters planned conflicting writes to %s", ErrConflict, write.Path)
				}
				continue
			}
			writes[write.Path] = write
		}
	}
	if len(writes) == 0 {
		return ConfigureResult{Instructions: instructions, RestartRequired: restart}, nil
	}

	paths := make([]string, 0, len(writes))
	for path := range writes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	tx, err := BeginTransaction(request.Home)
	if err != nil {
		return ConfigureResult{}, err
	}
	if err := tx.Allow(paths...); err != nil {
		return ConfigureResult{}, err
	}
	for _, path := range paths {
		if err := tx.Write(path, writes[path].Data); err != nil {
			return ConfigureResult{}, err
		}
	}
	receipt, err := tx.Commit(ReceiptMeta{Clients: append([]ClientID(nil), clients...), Model: request.Model, BridgeSHA256: request.BridgeSHA256})
	if err != nil {
		return ConfigureResult{}, err
	}
	return ConfigureResult{Receipt: receipt, Instructions: instructions, RestartRequired: restart}, nil
}

func validateConfigureRequest(request ConfigureRequest) error {
	if request.Home == "" || request.ServerURL == nil {
		return fmt.Errorf("%w: home and server URL are required", ErrUsage)
	}
	if _, err := NormalizeServerURL(request.ServerURL.String()); err != nil {
		return err
	}
	if request.Token.Reveal() == "" || hasControl(request.Token.Reveal()) {
		return fmt.Errorf("%w: token is required", ErrUsage)
	}
	if hasControl(request.Model) {
		return fmt.Errorf("%w: model contains control characters", ErrUsage)
	}
	return nil
}

func readPlannedFile(home, path string) ([]byte, bool, error) {
	inputHome, err := filepath.Abs(home)
	if err != nil {
		return nil, false, fmt.Errorf("%w: resolve planning home", ErrUnsafe)
	}
	canonical, err := canonicalHome(inputHome)
	if err != nil {
		return nil, false, err
	}
	rawPath, err := filepath.Abs(path)
	if err != nil {
		return nil, false, fmt.Errorf("%w: resolve planned configuration", ErrUnsafe)
	}
	rawPath = filepath.Clean(rawPath)
	if err := validateWithinHome(filepath.Clean(inputHome), rawPath); err != nil {
		return nil, false, err
	}
	if err := rejectSymlinksBelow(filepath.Clean(inputHome), rawPath); err != nil {
		return nil, false, err
	}
	resolved, err := canonicalPath(rawPath)
	if err != nil {
		return nil, false, err
	}
	if err := validateWithinHome(canonical, resolved); err != nil {
		return nil, false, err
	}
	data, err := secureReadFile(canonical, resolved)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: read client configuration", ErrUnsafe)
	}
	return data, true, nil
}

func serverOrigin(server *url.URL) string {
	normalized, err := NormalizeServerURL(server.String())
	if err != nil {
		return server.String()
	}
	return normalized.String()
}

func endpointV1(server *url.URL) string { return serverOrigin(server) + "/v1" }

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
