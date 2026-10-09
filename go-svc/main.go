// go-svc exposes a read-only summary of the Kubernetes cluster it runs in:
// server version, nodes, and namespaces, optionally scoped to one namespace.
// It is consumed by the k8sagnet frontend.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type NodeInfo struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Roles      string `json:"roles"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	CPU        string `json:"cpu"`
	Memory     string `json:"memory"`
	InternalIP string `json:"internalIP"`
	Age        string `json:"age"`
}

type ClusterInfo struct {
	ServerVersion string     `json:"serverVersion"`
	Platform      string     `json:"platform"`
	NodeCount     int        `json:"nodeCount"`
	PodCount      int        `json:"podCount"`
	Scope         string     `json:"scope"` // "" means all namespaces
	Nodes         []NodeInfo `json:"nodes"`
	// Every namespace name, regardless of scope, so the UI can populate its
	// namespace picker from a scoped response.
	AllNamespaces []string  `json:"allNamespaces"`
	CollectedAt   time.Time `json:"collectedAt"`
}

// ResourceList is one page of one kind.
type ResourceList struct {
	Kind        string    `json:"kind"`
	Label       string    `json:"label"`
	Scope       string    `json:"scope"`
	Columns     []Column  `json:"columns"`
	StatusKey   string    `json:"statusKey,omitempty"`
	Items       []Row     `json:"items"`
	Page        int       `json:"page"`
	PageSize    int       `json:"pageSize"`
	Total       int       `json:"total"`
	TotalPages  int       `json:"totalPages"`
	CollectedAt time.Time `json:"collectedAt"`
}

// Namespace names are DNS-1123 labels; reject anything else before it reaches
// the API server.
var nsPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func validNamespace(ns string) bool {
	return len(ns) <= 63 && nsPattern.MatchString(ns)
}

type server struct {
	clientset *kubernetes.Clientset
	agent     *agent
}

// newClientset prefers the in-cluster service account, falling back to the
// local kubeconfig so the binary is runnable outside the cluster too.
func newClientset() (*kubernetes.Clientset, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("not running in-cluster (%v), falling back to kubeconfig", err)
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			rules, &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, err
		}
	}
	return kubernetes.NewForConfig(cfg)
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return d.Round(time.Second).String()
	case d < time.Hour:
		return d.Round(time.Minute).String()
	case d < 48*time.Hour:
		return d.Round(time.Hour).String()
	default:
		return strings.TrimSuffix(d.Round(24*time.Hour).String(), "h0m0s") + "h"
	}
}

func nodeStatus(n corev1.Node) string {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			if c.Status == corev1.ConditionTrue {
				return "Ready"
			}
			return "NotReady"
		}
	}
	return "Unknown"
}

// nodeRoles reads the conventional node-role.kubernetes.io/<role> labels.
func nodeRoles(n corev1.Node) string {
	var roles []string
	for k := range n.Labels {
		if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && r != "" {
			roles = append(roles, r)
		}
	}
	if len(roles) == 0 {
		return "<none>"
	}
	sort.Strings(roles)
	return strings.Join(roles, ",")
}

func internalIP(n corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

// collect gathers cluster state. An empty scope means all namespaces; a
// non-empty scope narrows namespaces and pod counts to that one namespace.
// Nodes are cluster-scoped and always returned in full.
func (s *server) collect(ctx context.Context, scope string) (*ClusterInfo, error) {
	info := &ClusterInfo{CollectedAt: time.Now().UTC(), Scope: scope}

	version, err := s.clientset.Discovery().ServerVersion()
	if err != nil {
		return nil, err
	}
	info.ServerVersion = version.GitVersion
	info.Platform = version.Platform

	nodes, err := s.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, n := range nodes.Items {
		info.Nodes = append(info.Nodes, NodeInfo{
			Name:       n.Name,
			Status:     nodeStatus(n),
			Roles:      nodeRoles(n),
			Version:    n.Status.NodeInfo.KubeletVersion,
			OS:         n.Status.NodeInfo.OSImage,
			Arch:       n.Status.NodeInfo.Architecture,
			CPU:        n.Status.Capacity.Cpu().String(),
			Memory:     n.Status.Capacity.Memory().String(),
			InternalIP: internalIP(n),
			Age:        age(n.CreationTimestamp.Time),
		})
	}
	info.NodeCount = len(info.Nodes)

	// metav1.NamespaceAll is "", so an empty scope counts pods cluster-wide.
	pods, err := s.clientset.CoreV1().Pods(scope).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	info.PodCount = len(pods.Items)

	namespaces, err := s.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, ns := range namespaces.Items {
		info.AllNamespaces = append(info.AllNamespaces, ns.Name)
	}

	return info, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("write response: %v", err)
	}
}

func (s *server) handleCluster(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("namespace")
	if scope != "" && !validNamespace(scope) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid namespace name"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	info, err := s.collect(ctx, scope)
	if err != nil {
		log.Printf("collect failed: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, info)
}

// handleKinds drives the frontend's tab strip.
func (s *server) handleKinds(w http.ResponseWriter, _ *http.Request) {
	type entry struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}
	out := make([]entry, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, entry{Key: k.key, Label: k.label})
	}
	writeJSON(w, http.StatusOK, map[string]any{"kinds": out})
}

const (
	defaultPageSize = 20
	maxPageSize     = 200
)

func intParam(r *http.Request, name string, fallback, min, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return fallback
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (s *server) handleResources(w http.ResponseWriter, r *http.Request) {
	spec, ok := kindByKey[r.PathValue("kind")]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown kind"})
		return
	}

	scope := r.URL.Query().Get("namespace")
	if scope != "" && !validNamespace(scope) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid namespace name"})
		return
	}

	page := intParam(r, "page", 1, 1, 1<<20)
	pageSize := intParam(r, "pageSize", defaultPageSize, 1, maxPageSize)

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	rows, err := spec.list(ctx, s.clientset, scope)
	if err != nil {
		log.Printf("list %s failed: %v", spec.key, err)
		// Surface RBAC denials as-is; the UI shows them per tab.
		status := http.StatusBadGateway
		if se, ok := err.(*apierrors.StatusError); ok {
			if code := int(se.ErrStatus.Code); code == http.StatusForbidden ||
				code == http.StatusUnauthorized || code == http.StatusNotFound {
				status = code
			}
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	sortRows(rows)

	total := len(rows)
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	end := min(start+pageSize, total)
	if start > total {
		start = total
	}

	// The namespace column is redundant once a namespace is selected.
	columns := spec.columns
	if scope == "" {
		columns = append([]Column{namespaceColumn}, columns...)
	}

	writeJSON(w, http.StatusOK, ResourceList{
		Kind:        spec.key,
		Label:       spec.label,
		Scope:       scope,
		Columns:     columns,
		StatusKey:   spec.statusKey,
		Items:       rows[start:end],
		Page:        page,
		PageSize:    pageSize,
		Total:       total,
		TotalPages:  totalPages,
		CollectedAt: time.Now().UTC(),
	})
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func main() {
	clientset, err := newClientset()
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}
	s := &server{clientset: clientset, agent: newAgent()}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/cluster", s.handleCluster)
	mux.HandleFunc("GET /api/kinds", s.handleKinds)
	mux.HandleFunc("GET /api/resources/{kind}", s.handleResources)
	// Actions are validated and audited here, then executed by agent-svc.
	mux.HandleFunc("POST /api/actions", s.handleAction)
	mux.HandleFunc("GET /api/tasks/{id}", s.handleTask)
	mux.HandleFunc("GET /api/agent/status", s.handleAgentStatus)
	mux.HandleFunc("GET /healthz", handleHealthz)

	addr := ":" + envOr(os.Getenv("PORT"), "8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("cluster-info listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}

func envOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
