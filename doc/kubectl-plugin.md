# kubectl Plugin

## Install

```bash
just build-plugin
cp bin/kubectl-tofu /usr/local/bin/   # or anywhere on your PATH
```

Or via `go install`:
```bash
just install-plugin
```

## Commands

| Command | Description |
|---------|-------------|
| `kubectl tofu plan <project> [-n ns]` | Show plan output and status |
| `kubectl tofu approve <project> [-n ns]` | Approve a pending plan |
| `kubectl tofu logs <project> [--plan] [-n ns]` | Show logs of the latest job (use `--plan` for plan job) |
| `kubectl tofu suspend <project> [-n ns]` | Pause reconciliation |
| `kubectl tofu resume <project> [-n ns]` | Resume reconciliation |
| `kubectl tofu history <project> [-n ns]` | Show revision history |
| `kubectl tofu show <project> <revision> [-n ns]` | Show full details of a revision |
| `kubectl tofu pin <project> <revision> [-n ns]` | Pin to a stored revision for rollback |
| `kubectl tofu unpin <project> [-n ns]` | Resume normal flow (remove pin) |
