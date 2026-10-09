"""agent-svc: runs Claude Code headless to perform narrow cluster actions.

go-svc never deletes anything itself — it POSTs a task here, and this service
shells out to the Claude CLI, which uses kubectl with this pod's ServiceAccount.
The CLI's credentials come from an interactive `kubectl exec` login, persisted
on a PVC at $CLAUDE_CONFIG_DIR.

Guardrails, in order of trustworthiness (last is the one that actually binds):
  1. this service re-validates every request, never trusting its caller
  2. the CLI's managed settings allow only kubectl
  3. RBAC: the ServiceAccount can delete pods only, in allowed namespaces only
"""

import asyncio
import json
import os
import re
import uuid
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

app = FastAPI(title="agent-svc", version="0.7.0")

# Namespace: DNS-1123 label. Pod name: DNS-1123 subdomain.
NS_RE = re.compile(r"^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$")
NAME_RE = re.compile(r"^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$")

# Denied whatever the config says. A misconfigured allowlist must not be able
# to point the agent at the control plane.
HARD_DENY = {"kube-system", "kube-public", "kube-node-lease"}


def _env_set(name: str) -> set[str]:
    return {v.strip() for v in os.getenv(name, "").split(",") if v.strip()}


ALLOWED_NAMESPACES = _env_set("ALLOWED_NAMESPACES")
DENIED_NAMESPACES = HARD_DENY | _env_set("DENIED_NAMESPACES")
TASK_TIMEOUT = int(os.getenv("TASK_TIMEOUT_SECONDS", "120"))
TASK_TTL = int(os.getenv("TASK_TTL_SECONDS", "3600"))
CLAUDE_BIN = os.getenv("CLAUDE_BIN", "claude")
CONFIG_DIR = os.getenv("CLAUDE_CONFIG_DIR", "/home/node/.claude")

# kubectl resource names, which double as the UI's kind keys.
KIND_RE = re.compile(r"^[a-z][a-z0-9]*$")
SESSION_RE = re.compile(r"^[A-Za-z0-9-]{1,64}$")
MAX_PROMPT_CHARS = 2000

READ_TOOLS = [
    "Bash(kubectl get:*)",
    "Bash(kubectl describe:*)",
    "Bash(kubectl logs:*)",
    "Bash(kubectl top:*)",
    "Bash(kubectl explain:*)",
]


@dataclass
class ActionSpec:
    template: str
    allowed_tools: list[str]
    needs_prompt: bool = False


# The delete template is fixed — only validated identifiers are interpolated.
# The ask template carries a user question, so its safety comes from the tool
# allowlist below and from RBAC, not from the wording of the prompt.
ACTIONS: dict[str, ActionSpec] = {
    "delete-pod": ActionSpec(
        template=(
            "You are operating inside a Kubernetes cluster and kubectl is available "
            "and already authenticated as this pod's ServiceAccount.\n\n"
            "Task: the pod {name} in namespace {namespace} needs to be deleted.\n"
            "1. Run `kubectl get pod {name} -n {namespace} -o wide` and note its status.\n"
            "2. If it looks unhealthy, briefly say why (check recent events if useful).\n"
            "3. Delete it: `kubectl delete pod {name} -n {namespace}`.\n"
            "4. Confirm it is gone or being replaced.\n\n"
            "Run ONE plain kubectl command per tool call — pipes, redirects and "
            "helpers like grep/sed are refused; use kubectl's own flags.\n"
            "Act only on that exact pod in that exact namespace. Do not modify or "
            "delete anything else. Finish with one line starting with 'RESULT:'."
        ),
        allowed_tools=READ_TOOLS + ["Bash(kubectl delete pod:*)"],
    ),
    # Read-only: no delete tool is offered, and the ServiceAccount cannot read
    # Secrets, so "print the secret values" fails at the API server.
    "ask": ActionSpec(
        template=(
            "You are a read-only Kubernetes assistant with kubectl available, "
            "already authenticated as this pod's ServiceAccount.\n\n"
            "The user is asking about this resource:\n"
            "  kind:      {kind}\n"
            "  name:      {name}\n"
            "  namespace: {namespace}\n\n"
            "Answer their question by inspecting that resource with read-only "
            "kubectl commands (get, describe, logs). You may look at closely "
            "related objects when it helps, but stay in namespace {namespace}. "
            "Never modify or delete anything.\n\n"
            "Run ONE plain kubectl command per tool call. Pipes, redirects, "
            "`&&`, and helpers like grep/sed/head are not permitted and will be "
            "refused — use kubectl's own flags (-o wide, -o yaml, -o jsonpath) "
            "instead. If a command is refused, or RBAC denies it, or the object "
            "does not exist, say so plainly rather than trying to work around "
            "it.\n\n"
            "Be concise. Use a short table or list when it fits, and show the "
            "relevant kubectl output rather than describing it.\n\n"
            "Question: {prompt}"
        ),
        allowed_tools=READ_TOOLS,
        needs_prompt=True,
    ),
}


class TaskRequest(BaseModel):
    action: str
    namespace: str
    name: str
    kind: str | None = None
    prompt: str | None = None
    sessionId: str | None = None
    requestId: str | None = None


@dataclass
class Task:
    id: str
    action: str
    namespace: str
    name: str
    kind: str = ""
    prompt: str = ""
    status: str = "queued"  # queued|running|succeeded|failed|timeout
    transcript: str = ""
    error: str = ""
    sessionId: str = ""  # returned by the CLI; pass back to continue the thread
    permissionDenials: list = field(default_factory=list)
    requestId: str = ""
    createdAt: str = field(default_factory=lambda: datetime.now(timezone.utc).isoformat())
    startedAt: str = ""
    finishedAt: str = ""


_tasks: dict[str, Task] = {}
_current: str | None = None
_lock = asyncio.Lock()


def now() -> str:
    return datetime.now(timezone.utc).isoformat()


def check_policy(namespace: str, name: str) -> None:
    if not NS_RE.match(namespace):
        raise HTTPException(status_code=400, detail="invalid namespace name")
    if not NAME_RE.match(name):
        raise HTTPException(status_code=400, detail="invalid object name")
    if namespace in DENIED_NAMESPACES:
        raise HTTPException(status_code=403, detail=f"namespace '{namespace}' is denied")
    if namespace not in ALLOWED_NAMESPACES:
        raise HTTPException(status_code=403, detail=f"namespace '{namespace}' is not allowed")


def reap_tasks() -> None:
    """Drop finished tasks past their TTL; the durable record is go-svc's audit log."""
    cutoff = datetime.now(timezone.utc).timestamp() - TASK_TTL
    for tid, task in list(_tasks.items()):
        if task.status in ("succeeded", "failed", "timeout"):
            ts = datetime.fromisoformat(task.finishedAt or task.createdAt).timestamp()
            if ts < cutoff:
                _tasks.pop(tid, None)


def parse_cli_output(raw: str) -> tuple[str, str, list]:
    """Pull (answer, session id, permission denials) out of --output-format json.

    Falls back to the raw text if the CLI ever prints something else, so a
    schema change degrades to a readable transcript rather than an empty one.
    """
    raw = raw.strip()
    try:
        data = json.loads(raw)
    except (ValueError, TypeError):
        return raw, "", []
    if not isinstance(data, dict):
        return raw, "", []
    answer = data.get("result") or raw
    denials = data.get("permission_denials") or []
    return str(answer).strip(), str(data.get("session_id") or ""), denials


async def run_task(task: Task) -> None:
    """Invoke the Claude CLI in print mode and record everything it said."""
    global _current
    spec = ACTIONS[task.action]
    task.status = "running"
    task.startedAt = now()
    prompt = spec.template.format(
        namespace=task.namespace,
        name=task.name,
        kind=task.kind or "pods",
        prompt=task.prompt,
    )

    argv = [CLAUDE_BIN, "-p", prompt, "--output-format", "json"]
    # Per-invocation allowlist: the ask action is never handed a delete tool.
    for tool in spec.allowed_tools:
        argv += ["--allowedTools", tool]
    if task.sessionId:
        argv += ["--resume", task.sessionId]

    try:
        proc = await asyncio.create_subprocess_exec(
            *argv,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.STDOUT,
            cwd="/app",
            env=os.environ.copy(),
        )
    except FileNotFoundError:
        task.status = "failed"
        task.error = f"{CLAUDE_BIN} not found in image"
        task.finishedAt = now()
        _current = None
        return

    try:
        stdout, _ = await asyncio.wait_for(proc.communicate(), timeout=TASK_TIMEOUT)
        answer, session, denials = parse_cli_output(stdout.decode(errors="replace"))
        task.transcript = answer
        task.permissionDenials = denials
        # Keep the previous session id if the CLI did not report a new one.
        task.sessionId = session or task.sessionId
        if proc.returncode == 0:
            task.status = "succeeded"
        else:
            task.status = "failed"
            task.error = f"claude exited with code {proc.returncode}"
    except asyncio.TimeoutError:
        proc.kill()
        await proc.wait()
        task.status = "timeout"
        task.error = f"agent exceeded {TASK_TIMEOUT}s"
    except Exception as exc:  # noqa: BLE001 - surface anything to the caller
        task.status = "failed"
        task.error = str(exc)
    finally:
        task.finishedAt = now()
        _current = None


@app.post("/agent/tasks", status_code=202)
async def create_task(req: TaskRequest):
    global _current
    spec = ACTIONS.get(req.action)
    if spec is None:
        raise HTTPException(status_code=400, detail=f"unknown action '{req.action}'")
    check_policy(req.namespace, req.name)

    prompt = (req.prompt or "").strip()
    if spec.needs_prompt:
        if not prompt:
            raise HTTPException(status_code=400, detail="prompt is required")
        if len(prompt) > MAX_PROMPT_CHARS:
            raise HTTPException(
                status_code=400, detail=f"prompt exceeds {MAX_PROMPT_CHARS} characters"
            )
    kind = (req.kind or "").strip()
    if kind and not KIND_RE.match(kind):
        raise HTTPException(status_code=400, detail="invalid kind")
    # argv is built without a shell, but keep the session id to a safe shape.
    if req.sessionId and not SESSION_RE.match(req.sessionId):
        raise HTTPException(status_code=400, detail="invalid session id")

    async with _lock:
        # One task at a time: the CLI is a singleton with one login.
        if _current is not None and _tasks[_current].status in ("queued", "running"):
            raise HTTPException(status_code=409, detail="agent is busy with another task")
        reap_tasks()
        task = Task(
            id=uuid.uuid4().hex[:12],
            action=req.action,
            namespace=req.namespace,
            name=req.name,
            kind=kind,
            prompt=prompt,
            sessionId=req.sessionId or "",
            requestId=req.requestId or "",
        )
        _tasks[task.id] = task
        _current = task.id

    asyncio.create_task(run_task(task))
    return asdict(task)


@app.get("/agent/tasks/{task_id}")
def get_task(task_id: str):
    task = _tasks.get(task_id)
    if task is None:
        raise HTTPException(status_code=404, detail="unknown task")
    return asdict(task)


@app.get("/agent/status")
def status():
    """Advisory only — never used to block a request.

    The credential check is a heuristic on the CLI's config directory, so it
    reports what it looked at rather than asserting a verdict.
    """
    cred = Path(CONFIG_DIR) / ".credentials.json"
    api_key = bool(os.getenv("ANTHROPIC_API_KEY"))
    busy = _current is not None and _tasks.get(_current, Task("", "", "", "")).status in (
        "queued",
        "running",
    )
    return {
        "authenticated": api_key or cred.exists(),
        "authSource": "api-key" if api_key else ("cli-login" if cred.exists() else "none"),
        "configDir": CONFIG_DIR,
        "credentialFileFound": cred.exists(),
        "busy": busy,
        "actions": sorted(ACTIONS),
        "allowedNamespaces": sorted(ALLOWED_NAMESPACES),
        "deniedNamespaces": sorted(DENIED_NAMESPACES),
        "taskTimeoutSeconds": TASK_TIMEOUT,
    }


@app.get("/healthz")
def healthz():
    return {"status": "ok"}
