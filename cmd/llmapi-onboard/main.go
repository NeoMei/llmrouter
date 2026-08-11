package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/internal/onboarding"
	"golang.org/x/term"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	home, args, err := extractHome(args)
	if err != nil {
		return report(err)
	}
	if home == "" {
		home, err = os.UserHomeDir()
		if err != nil {
			return report(fmt.Errorf("%w: home directory unavailable", onboarding.ErrUnsafe))
		}
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		usage(os.Stdout)
		return 0
	}

	switch args[0] {
	case "install":
		env := environment()
		options, err := onboarding.ResolveOptions(onboarding.Inputs{Args: args[1:], Env: env, PageOrigin: env["LLMAPI_PAGE_ORIGIN"], Prompt: terminalPrompter{}})
		if err != nil {
			return report(err)
		}
		_ = options
		return report(fmt.Errorf("%w: no client adapters are registered", onboarding.ErrUnavailable))
	case "doctor":
		if len(args) != 1 {
			return report(fmt.Errorf("%w: doctor accepts no options", onboarding.ErrUsage))
		}
		if _, err := onboarding.BeginTransaction(home); err != nil {
			return report(err)
		}
		fmt.Fprintln(os.Stdout, "onboarding state is safe")
		return 0
	case "restore", "uninstall":
		id, err := receiptID(home, args[1:])
		if err != nil {
			return report(err)
		}
		if args[0] == "restore" {
			err = onboarding.Restore(home, id)
		} else {
			err = onboarding.Uninstall(home, id)
		}
		if err != nil {
			return report(err)
		}
		fmt.Fprintln(os.Stdout, "configuration restored")
		return 0
	case "receipts":
		if len(args) != 1 {
			return report(fmt.Errorf("%w: receipts accepts no options", onboarding.ErrUsage))
		}
		receipts, err := onboarding.ListReceipts(home)
		if err != nil {
			return report(err)
		}
		for _, receipt := range receipts {
			fmt.Fprintf(os.Stdout, "%s\t%s\n", receipt.ID, receipt.CreatedAt.Format("2006-01-02T15:04:05Z"))
		}
		return 0
	default:
		return report(fmt.Errorf("%w: unknown command", onboarding.ErrUsage))
	}
}

func extractHome(args []string) (string, []string, error) {
	var home string
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg != "--home" && !strings.HasPrefix(arg, "--home=") {
			remaining = append(remaining, arg)
			continue
		}
		value := ""
		if strings.HasPrefix(arg, "--home=") {
			value = strings.TrimPrefix(arg, "--home=")
		} else {
			index++
			if index >= len(args) {
				return "", nil, fmt.Errorf("%w: --home requires a value", onboarding.ErrUsage)
			}
			value = args[index]
		}
		if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return "", nil, fmt.Errorf("%w: invalid --home", onboarding.ErrUsage)
		}
		if home != "" {
			return "", nil, fmt.Errorf("%w: --home specified more than once", onboarding.ErrUsage)
		}
		home = value
	}
	return home, remaining, nil
}

func receiptID(home string, args []string) (string, error) {
	if len(args) > 1 {
		return "", fmt.Errorf("%w: expected at most one receipt ID", onboarding.ErrUsage)
	}
	if len(args) == 1 {
		return args[0], nil
	}
	receipts, err := onboarding.ListReceipts(home)
	if err != nil {
		return "", err
	}
	if len(receipts) == 0 {
		return "", fmt.Errorf("%w: no receipts found", onboarding.ErrUsage)
	}
	return receipts[0].ID, nil
}

type terminalPrompter struct{}

func (terminalPrompter) ReadHidden(label string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("interactive terminal unavailable")
	}
	fmt.Fprintf(os.Stderr, "%s: ", label)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(value), err
}

func (terminalPrompter) SelectMany(label string, allowed []onboarding.ClientID) ([]onboarding.ClientID, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("interactive terminal unavailable")
	}
	available := make([]string, len(allowed))
	for index, client := range allowed {
		available[index] = string(client)
	}
	fmt.Fprintf(os.Stderr, "%s (%s): ", label, strings.Join(available, ", "))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSpace(line), ",")
	clients := make([]onboarding.ClientID, 0, len(parts))
	for _, part := range parts {
		clients = append(clients, onboarding.ClientID(strings.TrimSpace(part)))
	}
	return clients, nil
}

func environment() map[string]string {
	values := make(map[string]string)
	for _, pair := range os.Environ() {
		key, value, found := strings.Cut(pair, "=")
		if found {
			values[key] = value
		}
	}
	return values
}

func report(err error) int {
	switch {
	case errors.Is(err, onboarding.ErrUsage):
		fmt.Fprintln(os.Stderr, "usage error")
		return 2
	case errors.Is(err, onboarding.ErrUnsafe), errors.Is(err, onboarding.ErrConflict):
		fmt.Fprintln(os.Stderr, "unsafe filesystem or configuration conflict")
		return 3
	case errors.Is(err, onboarding.ErrNetwork):
		fmt.Fprintln(os.Stderr, "network or authentication failure")
		return 4
	case errors.Is(err, onboarding.ErrUnavailable):
		fmt.Fprintln(os.Stderr, "client adapters are not registered")
		return 1
	default:
		fmt.Fprintln(os.Stderr, "unexpected onboarding failure")
		return 1
	}
}

func usage(output *os.File) {
	fmt.Fprintln(output, "usage: llmapi-onboard [--home DIR] install|doctor|restore|uninstall|receipts")
}
