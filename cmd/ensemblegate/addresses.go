package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seanbrill/local-llm/internal/ensemble"
)

// The no-socket mode (docs/ENSEMBLE.md §11, "A Conductor without the engine
// socket"). An app that starts containers itself (FileFreak's CLI) starts the
// members, then the Conductor with MEMBERS naming each one's address:
//
//	MEMBERS=chat=http://host.containers.internal:41001,see_image=http://host.containers.internal:41002
//
// The Conductor then holds no engine socket and starts nothing: it routes,
// runs the member tools and checks the members' health. A fault in it can
// reach the members' HTTP ports and nothing else on the computer. Starting,
// stopping and restarting members, and fitting them in memory, are the
// app's job, so there is no memory budget and no eviction.

// addressStartTimeout is how long a request waits for a member that isn't
// answering yet (the app may have just started it).
const addressStartTimeout = 3 * time.Minute

// parseMembers reads MEMBERS: tool=URL pairs, comma separated.
func parseMembers(v string) (map[string]*url.URL, error) {
	out := map[string]*url.URL{}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tool, raw, ok := strings.Cut(part, "=")
		tool, raw = strings.TrimSpace(tool), strings.TrimSpace(raw)
		if !ok || tool == "" || raw == "" {
			return nil, fmt.Errorf("MEMBERS: %q isn't tool=URL", part)
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("MEMBERS: the %s address %q isn't http://host:port", tool, raw)
		}
		u.Path = ""
		if _, dup := out[tool]; dup {
			return nil, fmt.Errorf("MEMBERS names %s twice", tool)
		}
		out[tool] = u
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("MEMBERS names no member")
	}
	return out, nil
}

// useAddresses switches the manager to the no-socket mode: members are
// reached at addrs and never started or stopped. It returns the manifest's
// members MEMBERS doesn't name (they can't be used), and errors for a tool
// that isn't one of the manifest's members.
func (mg *Manager) useAddresses(addrs map[string]*url.URL) (missing []string, err error) {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	for tool := range addrs {
		if _, ok := mg.members[tool]; !ok {
			return nil, fmt.Errorf("MEMBERS names %s, which isn't one of this ensemble's members (%s)", tool, strings.Join(mg.order, ", "))
		}
	}
	eng := addressEngine{byName: map[string]*url.URL{}, probe: mg.probeOnce}
	for _, tool := range mg.order {
		st := mg.members[tool]
		u, ok := addrs[tool]
		if !ok {
			missing = append(missing, tool)
			continue
		}
		st.addr = u.String()
		eng.byName[st.name] = u
	}
	mg.eng = eng
	mg.budget = 0 // the app fits the members in memory
	mg.startTimeout = addressStartTimeout
	mg.external = true
	return missing, nil
}

// addressEngine is the Engine of the no-socket mode: the members are already
// running somewhere, started by someone else.
type addressEngine struct {
	byName map[string]*url.URL
	probe  func(ctx context.Context, base string) (bool, string)
}

func (e addressEngine) Run(_ context.Context, spec RunSpec) error {
	if _, ok := e.byName[spec.Name]; !ok {
		return fmt.Errorf("this Conductor starts no containers (MEMBERS is set) and MEMBERS gives no address for the %s member", spec.Labels[ensemble.LabelTool])
	}
	return nil // started by the app: the health check says when it answers
}

func (e addressEngine) Remove(context.Context, string) error { return nil }

// HostPort is the address's port, which the Conductor shows in its status.
func (e addressEngine) HostPort(_ context.Context, name string) (int, error) {
	u, ok := e.byName[name]
	if !ok {
		return 0, fmt.Errorf("no address for %s", name)
	}
	if p := u.Port(); p != "" {
		return strconv.Atoi(p)
	}
	if u.Scheme == "https" {
		return 443, nil
	}
	return 80, nil
}

// State is "running" when the member answers its health check: it's the
// only sign of life without the engine.
func (e addressEngine) State(ctx context.Context, name string) (string, error) {
	u, ok := e.byName[name]
	if !ok {
		return "", nil
	}
	if ok, _ := e.probe(ctx, u.String()); ok {
		return "running", nil
	}
	return "not answering at " + u.String(), nil
}

func (e addressEngine) List(context.Context, string) ([]Container, error) { return nil, nil }

func (e addressEngine) Logs(context.Context, string, int) string { return "" }

// memberAddresses lists MEMBERS for the log, in a stable order.
func memberAddresses(addrs map[string]*url.URL) string {
	var parts []string
	for tool, u := range addrs {
		parts = append(parts, tool+" at "+u.String())
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// hostPort is a host and port as a URL's authority.
func hostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}
