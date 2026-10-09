# k8s-agent

A Kubernetes dashboard with a browser UI, a read-only Go API, and an optional Claude Code agent for selected cluster actions.

## What is included

- `frontend/`: static UI served by nginx. It proxies `/api/` requests to the Go service when deployed with Helm.
- `go-svc/`: lists cluster information and Kubernetes resources, and validates and audits requests sent to the agent.
- `agent-svc/`: FastAPI wrapper around Claude Code and `kubectl`. It can answer questions about resources and delete pods in explicitly allowed namespaces.
- `charts/k8sagnet/`: Helm chart for all three services, RBAC, and optional ingress.
- `Dockerfile`: a standalone Claude Code CLI image; the Helm chart uses the service Dockerfiles instead.

## Prerequisites

- A Kubernetes cluster and `kubectl` access
- Helm 3 and Docker
- Images that your cluster can pull, or images loaded into your local cluster
- A Claude Code login or Anthropic API key if you enable agent actions

The Go service uses its in-cluster ServiceAccount when deployed with Helm. When run outside a cluster, it uses your local kubeconfig.

## Build and deploy

Build the three images from their respective directories:

```sh
docker build -t k8sagnet-frontend:0.2.0 ./frontend
docker build -t k8sagnet-go-svc:0.2.0 ./go-svc
docker build -t k8sagnet-agent-svc:0.6.0 ./agent-svc
```

Make the images available to your cluster. For a remote cluster, push them to a registry and set `frontend.image.repository`, `goSvc.image.repository`, and `agentSvc.image.repository` to the registry paths. The tags above match the chart defaults in `charts/k8sagnet/values.yaml`.

Review `agentSvc.policy.allowedNamespaces` before installing. Its default is `demo`; that namespace must exist if you want agent actions there. Then install the chart:

```sh
helm upgrade --install k8sagnet ./charts/k8sagnet \
  --namespace k8sagnet --create-namespace
kubectl -n k8sagnet port-forward svc/k8sagnet-frontend 8080:80
```

Open <http://localhost:8080>. For other release names, namespaces, image locations, or ingress settings, adjust the Helm command or provide a values file. Run `helm show values ./charts/k8sagnet` to see the available settings.

## Agent authentication and permissions

The agent runs with a separate ServiceAccount. Its Helm RBAC permits pod deletion only in namespaces listed under `agentSvc.policy.allowedNamespaces`; `kube-system`, `kube-public`, and `kube-node-lease` are always denied. The Go service has read-only RBAC. By default, its Secrets permission is enabled so the UI can show secret **key names**; set `goSvc.rbac.allowSecrets=false` to remove that permission.

After deployment, log in to Claude Code in the agent pod:

```sh
kubectl -n k8sagnet exec -it deploy/k8sagnet-agent-svc -- claude
```

The chart stores the login on a persistent volume by default. If you use an API key instead, provide `ANTHROPIC_API_KEY` to the agent deployment through your cluster's secret management. Do not commit credentials or kubeconfig files.

Set `agentSvc.enabled=false` to deploy only the dashboard and read-only API. The UI's action endpoints require the agent service.

## Local Go development

With Go 1.25 and a working kubeconfig:

```sh
cd go-svc
go run .
```

The API listens on port 8080 by default (`PORT` overrides it). Its endpoints include `/healthz`, `/api/cluster`, `/api/kinds`, and `/api/resources/{kind}`. The frontend's standalone Docker image returns 503 for `/api/` until a backend is configured; the Helm chart supplies that configuration.
