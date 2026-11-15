package harness

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GatewayModel is one running model container the gateway exposes.
type GatewayModel struct {
	ID        string `json:"id"`        // stable, human-readable, unique
	Name      string `json:"name"`      // display name for Open WebUI
	BaseID    string `json:"base_id"`   // ID before duplicate disambiguation
	Port      int    `json:"port"`      // published host port
	Ref       string `json:"ref"`       // image ref it runs
	Modality  string `json:"modality"`  // text | code | reasoning | vision | embedding
	Engine    string `json:"engine"`    // docker | podman
	Container string `json:"container"` // container name
	Persona   string `json:"persona,omitempty"`
	Created   int64  `json:"created"` // unix seconds (0 if unknown)
}

// gatewayModalities are the modalities served by llama.cpp's OpenAI API (the
// llmgate shim): chat, code, reasoning, vision and embeddings; plus running
// Ensembles, whose Conductor speaks the same chat API (and speech).
var gatewayModalities = map[string]bool{"text": true, "code": true, "reasoning": true, "vision": true, "embedding": true, "ensemble": true}

// GatewayModalities lists the modalities the gateway exposes.
func GatewayModalities() []string {
	return []string{"text", "code", "reasoning", "vision", "embedding", "ensemble"}
}

// RowLabel reads a label from a Containers() row.
func RowLabel(row map[string]any, key string) string { return rowLabel(row, key) }

// RowPort is the host port a container's 8080 is published on (0 if none).
func RowPort(row map[string]any) int { return rowPort(row) }

// RowRunning reports a running container row.
func RowRunning(row map[string]any) bool { return rowRunning(row) }

// rowLabel reads key from a container row's docker-style "k=v,k=v" Labels
// (builder normalizes podman's map into the same string).
func rowLabel(row map[string]any, key string) string {
	switch l := row["Labels"].(type) {
	case string:
		for _, part := range strings.Split(l, ",") {
			if i := strings.IndexByte(part, '='); i > 0 && part[:i] == key {
				return part[i+1:]
			}
		}
	case map[string]any:
		if v, ok := l[key].(string); ok {
			return v
		}
	}
	return ""
}

func rowString(row map[string]any, key string) string {
	s, _ := row[key].(string)
	return strings.TrimSpace(s)
}

func rowRunning(row map[string]any) bool {
	if strings.EqualFold(rowString(row, "State"), "running") {
		return true
	}
	return strings.HasPrefix(rowString(row, "Status"), "Up")
}

var (
	nameAsPortRe = regexp.MustCompile(`^/?localllm-(\d+)$`)
	// host port published for the container's 8080 (llmgate's port).
	portMapRe = regexp.MustCompile(`:(\d+)->8080/tcp`)
	idCharsRe = regexp.MustCompile(`[^a-z0-9._:@-]+`)
	hexIDRe   = regexp.MustCompile(`^(sha256:)?[0-9a-f]{12,64}$`)
)

// rowPort finds the host port a model container is reachable on: the factory
// names its containers localllm-<port>; otherwise read the 8080 port mapping.
func rowPort(row map[string]any) int {
	name := rowString(row, "Names")
	if i := strings.IndexByte(name, ','); i >= 0 {
		name = name[:i]
	}
	if m := nameAsPortRe.FindStringSubmatch(name); m != nil {
		p, _ := strconv.Atoi(m[1])
		return p
	}
	if m := portMapRe.FindStringSubmatch(rowString(row, "Ports")); m != nil {
		p, _ := strconv.Atoi(m[1])
		return p
	}
	return 0
}

// rowCreated reads the container's creation time (podman: unix number; docker:
// "2006-01-02 15:04:05 -0700 MST").
func rowCreated(row map[string]any) int64 {
	if f, ok := row["Created"].(float64); ok && f > 0 {
		return int64(f)
	}
	if t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", rowString(row, "CreatedAt")); err == nil {
		return t.Unix()
	}
	return 0
}

// splitRef splits "registry:5000/org/name:tag" into ("registry:5000/org/name", "tag").
func splitRef(ref string) (repo, tag string) {
	ref = strings.TrimSpace(ref)
	if i := strings.IndexByte(ref, '@'); i >= 0 { // drop a digest
		ref = ref[:i]
	}
	if i := strings.LastIndexByte(ref, ':'); i > 0 && !strings.Contains(ref[i+1:], "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// cleanID lowercases and keeps a conservative id alphabet.
func cleanID(s string) string {
	s = idCharsRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	return strings.Trim(s, "-.:@")
}

// baseID derives a stable, readable id from the image a container runs: the
// repo's last path segment ("local-llm/qwen3.5-9b:latest" -> "qwen3.5-9b"),
// plus ":<tag>" for a non-latest tag. Untagged images fall back to the catalog
// model id, then the container name.
func baseID(ref, modelID, container string) string {
	repo, tag := splitRef(ref)
	short := repo
	if i := strings.LastIndexByte(short, '/'); i >= 0 {
		short = short[i+1:]
	}
	if short == "" || hexIDRe.MatchString(strings.ToLower(ref)) {
		if id := cleanID(modelID); id != "" {
			return id
		}
		return cleanID(strings.TrimPrefix(container, "/"))
	}
	id := cleanID(short)
	if tag != "" && tag != "latest" {
		id += ":" + cleanID(tag)
	}
	if id == "" {
		id = cleanID(strings.TrimPrefix(container, "/"))
	}
	return id
}

// ModelsFromContainers turns `Containers()` rows into the gateway's model list:
// running, OpenAI-speaking (llama.cpp family), with a reachable host port.
// modalityOf resolves a catalog model id to its modality for images built
// before the modality label existed (may be nil). Duplicate ids (the same image
// run twice) get "@<port>" so every id is unique.
func ModelsFromContainers(rows []map[string]any, modalityOf func(modelID string) string) []GatewayModel {
	var out []GatewayModel
	for _, row := range rows {
		if !rowRunning(row) {
			continue
		}
		name := strings.TrimPrefix(rowString(row, "Names"), "/")
		if i := strings.IndexByte(name, ','); i >= 0 {
			name = name[:i]
		}
		// An Ensemble's Conductor starts and evicts its members (ens-<id>-<tool>)
		// on demand; exposing them would hand out models that vanish mid-chat.
		// The ensemble itself is listed (modality "ensemble").
		if strings.HasPrefix(name, "ens-") || rowLabel(row, "local-llm.ensemble-member") != "" {
			continue
		}
		switch rowLabel(row, "local-llm.runtime") {
		case "python", "audiocpp":
			continue
		}
		modelID := rowLabel(row, "local-llm.model")
		mod := rowLabel(row, "local-llm.modality")
		if mod == "" && modalityOf != nil {
			mod = modalityOf(modelID)
		}
		if mod == "" {
			mod = "text"
		}
		if !gatewayModalities[mod] {
			continue
		}
		port := rowPort(row)
		if port <= 0 {
			continue
		}
		ref := rowLabel(row, "local-llm.ref")
		if ref == "" {
			ref = rowString(row, "Image")
		}
		engine := rowString(row, "Engine")
		if engine == "" {
			engine = "docker"
		}
		id := baseID(strings.TrimPrefix(ref, "localhost/"), modelID, name)
		if id == "" {
			continue
		}
		out = append(out, GatewayModel{
			ID: id, BaseID: id, Port: port, Ref: ref, Modality: mod, Engine: engine,
			Container: name, Persona: rowLabel(row, "local-llm.persona"), Created: rowCreated(row),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BaseID != out[j].BaseID {
			return out[i].BaseID < out[j].BaseID
		}
		return out[i].Port < out[j].Port
	})
	count := map[string]int{}
	for _, m := range out {
		count[m.BaseID]++
	}
	for i := range out {
		m := &out[i]
		if count[m.BaseID] > 1 {
			m.ID = m.BaseID + "@" + strconv.Itoa(m.Port)
		}
		m.Name = displayName(*m, count[m.BaseID] > 1)
	}
	return out
}

func displayName(m GatewayModel, dup bool) string {
	var extra []string
	if m.Persona != "" {
		extra = append(extra, m.Persona)
	}
	switch m.Modality {
	case "embedding":
		extra = append(extra, "embeddings")
	case "ensemble":
		extra = append(extra, "ensemble")
	}
	if dup {
		extra = append(extra, "port "+strconv.Itoa(m.Port))
	}
	if len(extra) == 0 {
		return m.BaseID
	}
	return m.BaseID + " (" + strings.Join(extra, ", ") + ")"
}

// ResolveModel finds the model a request names. Exact ids win; a bare base id
// that became ambiguous (the same image now runs twice) still routes to the
// lowest port, so chats configured before the second instance keep working.
// The container name and image ref are accepted as aliases. An empty name
// routes only when exactly one model is running.
func ResolveModel(models []GatewayModel, name string) (GatewayModel, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		if len(models) == 1 {
			return models[0], true
		}
		return GatewayModel{}, false
	}
	for _, m := range models {
		if m.ID == name {
			return m, true
		}
	}
	lower := strings.ToLower(name)
	for _, m := range models { // sorted by base id, then port
		if m.BaseID == lower || strings.EqualFold(m.Container, name) || strings.EqualFold(m.Ref, name) {
			return m, true
		}
	}
	return GatewayModel{}, false
}
