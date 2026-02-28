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
| `kubectl tofu suspend <project> [-n ns]` | Pause reconciliation |
| `kubectl tofu resume <project> [-n ns]` | Resume reconciliation |
