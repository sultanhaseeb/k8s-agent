package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Column is one table column. The frontend renders whatever the server sends,
// so adding a kind here needs no frontend change.
type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// Row is a single object flattened to its column values.
type Row map[string]any

type kindSpec struct {
	key       string
	label     string
	columns   []Column
	statusKey string // column rendered as a coloured pill, if any
	list      func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error)
}

// cols builds columns from key/label pairs.
func cols(pairs ...string) []Column {
	out := make([]Column, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Column{Key: pairs[i], Label: pairs[i+1]})
	}
	return out
}

func deref32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// join renders a list compactly, summarising the tail past limit.
func join(values []string, limit int) string {
	out := values[:0:0]
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return ""
	}
	if len(out) > limit {
		return strings.Join(out[:limit], ", ") + fmt.Sprintf(" +%d", len(out)-limit)
	}
	return strings.Join(out, ", ")
}

func firstImage(containers []corev1.Container) string {
	if len(containers) == 0 {
		return ""
	}
	return containers[0].Image
}

func meta(m metav1.ObjectMeta) Row {
	return Row{"namespace": m.Namespace, "name": m.Name, "age": age(m.CreationTimestamp.Time)}
}

// with merges extra fields into a base row.
func with(base Row, extra Row) Row {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

// An empty namespace means all namespaces: metav1.NamespaceAll is "", so the
// same typed call covers both cases.
var kinds = []kindSpec{
	{
		key:   "pods",
		label: "Pods",
		columns: cols("name", "Name", "ready", "Ready", "status", "Status",
			"restarts", "Restarts", "node", "Node", "ip", "Pod IP", "age", "Age"),
		statusKey: "status",
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, p := range l.Items {
				ready, restarts := 0, 0
				for _, cs := range p.Status.ContainerStatuses {
					if cs.Ready {
						ready++
					}
					restarts += int(cs.RestartCount)
				}
				rows = append(rows, with(meta(p.ObjectMeta), Row{
					"ready":    fmt.Sprintf("%d/%d", ready, len(p.Status.ContainerStatuses)),
					"status":   string(p.Status.Phase),
					"restarts": restarts,
					"node":     p.Spec.NodeName,
					"ip":       p.Status.PodIP,
				}))
			}
			return rows, nil
		},
	},
	{
		key:   "deployments",
		label: "Deployments",
		columns: cols("name", "Name", "ready", "Ready", "uptodate", "Up-to-date",
			"available", "Available", "image", "Image", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, d := range l.Items {
				rows = append(rows, with(meta(d.ObjectMeta), Row{
					"ready":     fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, deref32(d.Spec.Replicas)),
					"uptodate":  d.Status.UpdatedReplicas,
					"available": d.Status.AvailableReplicas,
					"image":     firstImage(d.Spec.Template.Spec.Containers),
				}))
			}
			return rows, nil
		},
	},
	{
		key:     "statefulsets",
		label:   "StatefulSets",
		columns: cols("name", "Name", "ready", "Ready", "image", "Image", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, s := range l.Items {
				rows = append(rows, with(meta(s.ObjectMeta), Row{
					"ready": fmt.Sprintf("%d/%d", s.Status.ReadyReplicas, deref32(s.Spec.Replicas)),
					"image": firstImage(s.Spec.Template.Spec.Containers),
				}))
			}
			return rows, nil
		},
	},
	{
		key:   "daemonsets",
		label: "DaemonSets",
		columns: cols("name", "Name", "desired", "Desired", "ready", "Ready",
			"available", "Available", "image", "Image", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, d := range l.Items {
				rows = append(rows, with(meta(d.ObjectMeta), Row{
					"desired":   d.Status.DesiredNumberScheduled,
					"ready":     d.Status.NumberReady,
					"available": d.Status.NumberAvailable,
					"image":     firstImage(d.Spec.Template.Spec.Containers),
				}))
			}
			return rows, nil
		},
	},
	{
		key:     "replicasets",
		label:   "ReplicaSets",
		columns: cols("name", "Name", "desired", "Desired", "ready", "Ready", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, r := range l.Items {
				rows = append(rows, with(meta(r.ObjectMeta), Row{
					"desired": deref32(r.Spec.Replicas),
					"ready":   r.Status.ReadyReplicas,
				}))
			}
			return rows, nil
		},
	},
	{
		key:     "jobs",
		label:   "Jobs",
		columns: cols("name", "Name", "completions", "Completions", "failed", "Failed", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, j := range l.Items {
				want := deref32(j.Spec.Completions)
				if want == 0 {
					want = 1
				}
				rows = append(rows, with(meta(j.ObjectMeta), Row{
					"completions": fmt.Sprintf("%d/%d", j.Status.Succeeded, want),
					"failed":      j.Status.Failed,
				}))
			}
			return rows, nil
		},
	},
	{
		key:   "cronjobs",
		label: "CronJobs",
		columns: cols("name", "Name", "schedule", "Schedule", "suspend", "Suspended",
			"active", "Active", "last", "Last run", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.BatchV1().CronJobs(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, cj := range l.Items {
				last := ""
				if cj.Status.LastScheduleTime != nil {
					last = age(cj.Status.LastScheduleTime.Time)
				}
				suspended := false
				if cj.Spec.Suspend != nil {
					suspended = *cj.Spec.Suspend
				}
				rows = append(rows, with(meta(cj.ObjectMeta), Row{
					"schedule": cj.Spec.Schedule,
					"suspend":  fmt.Sprintf("%t", suspended),
					"active":   len(cj.Status.Active),
					"last":     last,
				}))
			}
			return rows, nil
		},
	},
	{
		key:   "services",
		label: "Services",
		columns: cols("name", "Name", "type", "Type", "clusterip", "Cluster IP",
			"ports", "Ports", "selector", "Selector", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, s := range l.Items {
				var ports, selector []string
				for _, p := range s.Spec.Ports {
					entry := fmt.Sprintf("%d/%s", p.Port, p.Protocol)
					if p.TargetPort.String() != "" && p.TargetPort.String() != "0" {
						entry += "->" + p.TargetPort.String()
					}
					ports = append(ports, entry)
				}
				for k, v := range s.Spec.Selector {
					selector = append(selector, k+"="+v)
				}
				sort.Strings(selector)
				rows = append(rows, with(meta(s.ObjectMeta), Row{
					"type":      string(s.Spec.Type),
					"clusterip": s.Spec.ClusterIP,
					"ports":     join(ports, 4),
					"selector":  join(selector, 2),
				}))
			}
			return rows, nil
		},
	},
	{
		key:   "ingresses",
		label: "Ingresses",
		columns: cols("name", "Name", "class", "Class", "hosts", "Hosts",
			"paths", "Paths", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, i := range l.Items {
				var hosts, paths []string
				for _, rule := range i.Spec.Rules {
					if rule.Host != "" {
						hosts = append(hosts, rule.Host)
					}
					if rule.HTTP != nil {
						for _, p := range rule.HTTP.Paths {
							if p.Path == "" {
								paths = append(paths, "/")
							} else {
								paths = append(paths, p.Path)
							}
						}
					}
				}
				rows = append(rows, with(meta(i.ObjectMeta), Row{
					"class": derefStr(i.Spec.IngressClassName),
					"hosts": join(hosts, 4),
					"paths": join(paths, 4),
				}))
			}
			return rows, nil
		},
	},
	{
		key:     "configmaps",
		label:   "ConfigMaps",
		columns: cols("name", "Name", "keycount", "Keys", "keys", "Key names", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, cm := range l.Items {
				var keys []string
				for k := range cm.Data {
					keys = append(keys, k)
				}
				for k := range cm.BinaryData {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				rows = append(rows, with(meta(cm.ObjectMeta), Row{
					"keycount": len(keys),
					"keys":     join(keys, 4),
				}))
			}
			return rows, nil
		},
	},
	{
		key:   "secrets",
		label: "Secrets",
		columns: cols("name", "Name", "type", "Type", "keycount", "Keys",
			"keys", "Key names", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, s := range l.Items {
				// Key names only: secret values never enter the response.
				var keys []string
				for k := range s.Data {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				rows = append(rows, with(meta(s.ObjectMeta), Row{
					"type":     string(s.Type),
					"keycount": len(keys),
					"keys":     join(keys, 4),
				}))
			}
			return rows, nil
		},
	},
	{
		key:     "serviceaccounts",
		label:   "ServiceAccounts",
		columns: cols("name", "Name", "secrets", "Secrets", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.CoreV1().ServiceAccounts(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, sa := range l.Items {
				rows = append(rows, with(meta(sa.ObjectMeta), Row{"secrets": len(sa.Secrets)}))
			}
			return rows, nil
		},
	},
	{
		key:   "persistentvolumeclaims",
		label: "PVCs",
		columns: cols("name", "Name", "status", "Status", "volume", "Volume",
			"capacity", "Capacity", "modes", "Access modes",
			"storageclass", "StorageClass", "age", "Age"),
		statusKey: "status",
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, p := range l.Items {
				capacity := ""
				if q, ok := p.Status.Capacity[corev1.ResourceStorage]; ok {
					capacity = q.String()
				}
				var modes []string
				for _, m := range p.Spec.AccessModes {
					modes = append(modes, string(m))
				}
				rows = append(rows, with(meta(p.ObjectMeta), Row{
					"status":       string(p.Status.Phase),
					"volume":       p.Spec.VolumeName,
					"capacity":     capacity,
					"modes":        join(modes, 3),
					"storageclass": derefStr(p.Spec.StorageClassName),
				}))
			}
			return rows, nil
		},
	},
	{
		key:     "endpoints",
		label:   "Endpoints",
		columns: cols("name", "Name", "addresses", "Addresses", "ports", "Ports", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.CoreV1().Endpoints(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, e := range l.Items {
				var addresses, ports []string
				seen := map[string]bool{}
				for _, sub := range e.Subsets {
					for _, a := range sub.Addresses {
						addresses = append(addresses, a.IP)
					}
					for _, p := range sub.Ports {
						v := fmt.Sprintf("%d", p.Port)
						if !seen[v] {
							seen[v] = true
							ports = append(ports, v)
						}
					}
				}
				sort.Strings(ports)
				rows = append(rows, with(meta(e.ObjectMeta), Row{
					"addresses": join(addresses, 4),
					"ports":     join(ports, 6),
				}))
			}
			return rows, nil
		},
	},
	{
		key:   "horizontalpodautoscalers",
		label: "HPAs",
		columns: cols("name", "Name", "reference", "Reference", "min", "Min",
			"max", "Max", "replicas", "Replicas", "age", "Age"),
		list: func(ctx context.Context, c *kubernetes.Clientset, ns string) ([]Row, error) {
			l, err := c.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return nil, err
			}
			rows := make([]Row, 0, len(l.Items))
			for _, h := range l.Items {
				min := deref32(h.Spec.MinReplicas)
				if min == 0 {
					min = 1
				}
				rows = append(rows, with(meta(h.ObjectMeta), Row{
					"reference": fmt.Sprintf("%s/%s", h.Spec.ScaleTargetRef.Kind, h.Spec.ScaleTargetRef.Name),
					"min":       min,
					"max":       h.Spec.MaxReplicas,
					"replicas":  h.Status.CurrentReplicas,
				}))
			}
			return rows, nil
		},
	},
}

var kindByKey = func() map[string]kindSpec {
	m := make(map[string]kindSpec, len(kinds))
	for _, k := range kinds {
		m[k.key] = k
	}
	return m
}()

var namespaceColumn = Column{Key: "namespace", Label: "Namespace"}

// sortRows gives pagination a stable order: namespace, then name.
func sortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		ni, _ := rows[i]["namespace"].(string)
		nj, _ := rows[j]["namespace"].(string)
		if ni != nj {
			return ni < nj
		}
		a, _ := rows[i]["name"].(string)
		b, _ := rows[j]["name"].(string)
		return a < b
	})
}
