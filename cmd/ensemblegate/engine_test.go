package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fake engine CLI: when ENSEMBLEGATE_FAKE_DOCKER names a state file, this
// test binary acts as `docker` (run, rm, port, inspect, ps, logs) over that
// file, so cliEngine runs its real commands without a container engine.

const fakeDockerEnv = "ENSEMBLEGATE_FAKE_DOCKER"

func TestMain(m *testing.M) {
	if state := os.Getenv(fakeDockerEnv); state != "" {
		os.Exit(fakeDocker(state, os.Args[1:]))
	}
	os.Exit(m.Run())
}

type dockerState struct {
	Containers map[string]dockerCtr `json:"containers"`
	Calls      [][]string           `json:"calls"`
}

type dockerCtr struct {
	Image  string            `json:"image"`
	Labels map[string]string `json:"labels"`
	Port   string            `json:"port"`
	State  string            `json:"state"`
}

// lockState serializes fake docker processes over the state file (StopAll
// runs several `docker rm` at once), so no update is lost.
func lockState(path string) func() {
	lock := path + ".lock"
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
			f.Close()
			return func() { _ = os.Remove(lock) }
		}
	}
	return func() {}
}

func fakeDocker(path string, args []string) int {
	defer lockState(path)() // registered first, so it unlocks after the state is written
	var st dockerState
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Containers == nil {
		st.Containers = map[string]dockerCtr{}
	}
	st.Calls = append(st.Calls, args)
	defer func() {
		b, _ := json.Marshal(st)
		_ = os.WriteFile(path, b, 0o600)
	}()
	noSuch := func(name string) int {
		fmt.Fprintf(os.Stderr, "Error response from daemon: No such container: %s\n", name)
		return 1
	}
	switch args[0] {
	case "run":
		c := dockerCtr{Labels: map[string]string{}, State: "running"}
		name := ""
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "-d":
			case "--name":
				i++
				name = args[i]
			case "--label":
				i++
				k, v, _ := strings.Cut(args[i], "=")
				c.Labels[k] = v
			case "-p":
				i++
				if strings.HasPrefix(args[i], "127.0.0.1::") {
					c.Port = "49153"
					if p := os.Getenv(fakeDockerEnv + "_PORT"); p != "" {
						c.Port = p
					}
				} else {
					c.Port = strings.Split(args[i], ":")[1]
				}
			case "--gpus", "--device":
				i++
			default:
				c.Image = args[i]
			}
		}
		if c.Image == "local-llm/crash" {
			c.State = "exited"
		}
		if c.Image == "local-llm/missing" {
			fmt.Fprintln(os.Stderr, "Unable to find image 'local-llm/missing:latest' locally")
			return 125
		}
		st.Containers[name] = c
		fmt.Println("0123456789abcdef")
	case "rm":
		name := args[len(args)-1]
		if _, ok := st.Containers[name]; !ok {
			return noSuch(name)
		}
		delete(st.Containers, name)
		fmt.Println(name)
	case "port":
		c, ok := st.Containers[args[1]]
		if !ok {
			return noSuch(args[1])
		}
		fmt.Printf("127.0.0.1:%s\n[::1]:%s\n", c.Port, c.Port)
	case "inspect":
		name := args[len(args)-1]
		c, ok := st.Containers[name]
		if !ok {
			return noSuch(name)
		}
		fmt.Println(c.State)
	case "ps":
		filter := ""
		for i, a := range args {
			if a == "--filter" {
				filter = strings.TrimPrefix(args[i+1], "label=")
			}
		}
		k, v, _ := strings.Cut(filter, "=")
		for name, c := range st.Containers {
			if c.Labels[k] != v {
				continue
			}
			var labels []string
			for lk, lv := range c.Labels {
				labels = append(labels, lk+"="+lv)
			}
			b, _ := json.Marshal(map[string]any{"Names": name, "Image": c.Image, "State": c.State, "Labels": strings.Join(labels, ",")})
			fmt.Println(string(b))
		}
	case "logs":
		fmt.Println("line one\nline two")
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		return 2
	}
	return 0
}

func readDockerState(t *testing.T, path string) dockerState {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st dockerState
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestCLIEngine(t *testing.T) {
	state := t.TempDir() + "/docker.json"
	t.Setenv(fakeDockerEnv, state)
	eng := cliEngine{bin: os.Args[0]}
	ctx := context.Background()

	spec := RunSpec{Name: "ens-x-chat", Image: "local-llm/qwen", GPU: "cuda",
		Labels: map[string]string{"local-llm.ensemble-tool": "chat", "local-llm.ensemble-member": "x"}}
	if err := eng.Run(ctx, spec); err != nil {
		t.Fatal(err)
	}
	st := readDockerState(t, state)
	want := []string{"run", "-d", "--name", "ens-x-chat", "--label", "local-llm.ensemble-member=x", "--label", "local-llm.ensemble-tool=chat",
		"-p", "127.0.0.1::8080", "--gpus", "all", "local-llm/qwen"}
	if !reflect.DeepEqual(st.Calls[0], want) {
		t.Errorf("run args = %q", st.Calls[0])
	}
	if p, err := eng.HostPort(ctx, "ens-x-chat"); err != nil || p != 49153 {
		t.Errorf("port = %d, %v", p, err)
	}
	if s, err := eng.State(ctx, "ens-x-chat"); err != nil || s != "running" {
		t.Errorf("state = %q, %v", s, err)
	}
	cs, err := eng.List(ctx, "local-llm.ensemble-member=x")
	if err != nil || len(cs) != 1 || cs[0].Name != "ens-x-chat" || cs[0].Labels["local-llm.ensemble-tool"] != "chat" || cs[0].Image != "local-llm/qwen" {
		t.Errorf("list = %+v, %v", cs, err)
	}
	if logs := eng.Logs(ctx, "ens-x-chat", 5); logs != "line one\nline two" {
		t.Errorf("logs = %q", logs)
	}
	if err := eng.Remove(ctx, "ens-x-chat"); err != nil {
		t.Fatal(err)
	}
	// Gone: removing again is fine, its state is "" and its port an error.
	if err := eng.Remove(ctx, "ens-x-chat"); err != nil {
		t.Errorf("remove twice: %v", err)
	}
	if s, err := eng.State(ctx, "ens-x-chat"); err != nil || s != "" {
		t.Errorf("state of a removed container = %q, %v", s, err)
	}
	if _, err := eng.HostPort(ctx, "ens-x-chat"); err == nil {
		t.Error("port of a removed container")
	}
	err = eng.Run(ctx, RunSpec{Name: "ens-x-tts", Image: "local-llm/missing", Port: 18090, GPU: "vulkan"})
	if err == nil || !strings.Contains(err.Error(), "Unable to find image") {
		t.Errorf("run of a missing image: %v", err)
	}
	st = readDockerState(t, state)
	last := st.Calls[len(st.Calls)-1]
	if strings.Join(last, " ") != "run -d --name ens-x-tts -p 127.0.0.1:18090:8080 --device /dev/dri local-llm/missing" {
		t.Errorf("pinned port + vulkan args = %q", last)
	}
}

// TestManagerOverCLIEngine runs the lifecycle through the fake CLI: a member
// starts and answers; one whose container exits while loading fails with its
// logs and is cleaned up.
func TestManagerOverCLIEngine(t *testing.T) {
	state := t.TempDir() + "/docker.json"
	t.Setenv(fakeDockerEnv, state)
	te := newTestEns(t)
	t.Setenv(fakeDockerEnv+"_PORT", strconv.Itoa(portOf(te.eng.images["local-llm/chat"])))
	mg := te.g.mg
	mg.eng = cliEngine{bin: os.Args[0]}
	mg.startTimeout = 60 * time.Second
	if err := mg.Start(context.Background(), "chat"); err != nil {
		t.Fatal(err)
	}
	if s := memberState(t, mg, "chat"); s.State != "ready" {
		t.Fatalf("chat = %+v", s)
	}

	te.image("local-llm/crash", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Setenv(fakeDockerEnv+"_PORT", strconv.Itoa(portOf(te.eng.images["local-llm/crash"])))
	mg.members["speak"].m.Image = "local-llm/crash"
	err := mg.Start(context.Background(), "speak")
	if err == nil || !strings.Contains(err.Error(), "its container exited") || !strings.Contains(err.Error(), "line two") {
		t.Errorf("err = %v; want the exit and the container's logs", err)
	}
	st := readDockerState(t, state)
	if _, left := st.Containers["ens-test-speak"]; left {
		t.Error("the crashed member's container was left")
	}
	mg.StopAll(context.Background())
	if st := readDockerState(t, state); len(st.Containers) != 0 {
		t.Errorf("containers left after StopAll: %v", st.Containers)
	}
}
