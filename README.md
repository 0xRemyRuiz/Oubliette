# oubliette

Transparent live migration of a running process from a Linux host into a
pre-existing KVM guest. The process does not observe the transition: from its
perspective a syscall returns normally inside the guest.

The shadow VM is an input to this tool, not an output. The caller is responsible
for ensuring the guest exposes the same filesystem paths the process expects
(same CWD, same open file paths, same hostname if the process inspects it).

## Prerequisites

| Dependency | Minimum version | Notes |
|------------|-----------------|-------|
| Linux kernel (host) | 5.4 | CRIU requires `CONFIG_CHECKPOINT_RESTORE=y` |
| Linux kernel (guest) | 5.4 | Same kernel config as host |
| CRIU | **3.15** | First stable release with `--shell-job` + `--detach` |
| QEMU/KVM | 6.0 | Guest must be a running libvirt domain |
| virsh | any (libvirt ≥ 7.0) | Used for domain state and IP discovery |
| ssh / scp | OpenSSH ≥ 7.0 | Host tool; guest must have an sshd running |

> **Important:** oubliette must run as root on the host. CRIU dump requires
> `CAP_SYS_PTRACE` and `CAP_NET_ADMIN` at minimum.

## Installation

```sh
go install github.com/oubliette/oubliette/cmd/oubliette@latest
```

Or build from source:

```sh
git clone https://github.com/oubliette/oubliette
cd oubliette
go build -o oubliette ./cmd/oubliette
```

## Usage

```sh
oubliette --pid <pid> --vm <domain-name> [--config <path>] [--log-level <level>]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--pid` | *(required)* | PID of the process to migrate |
| `--vm` | *(required)* | libvirt domain name of the target VM |
| `--config` | `oubliette.yaml` | Path to the YAML config file |
| `--log-level` | `info` | Verbosity: `debug`, `info`, `warn`, `error` |

## Configuration

Config file is YAML. All fields are optional except `vm.ssh_user` and `vm.ssh_key_path`.

```yaml
# criu_path: path to criu binary on the host. Default: "criu" (looked up in PATH).
criu_path: /usr/sbin/criu

# local_dump_dir: directory on the host where CRIU writes checkpoint images.
# Created automatically if it does not exist. Default: /tmp/oubliette-dump.
local_dump_dir: /var/run/oubliette/dump

vm:
  # ssh_user: SSH login username on the guest. Required.
  ssh_user: root

  # ssh_key_path: path to the SSH private key for guest access. Required.
  ssh_key_path: /root/.ssh/id_ed25519

  # ssh_port: TCP port sshd listens on inside the guest. Default: 22.
  ssh_port: 22

  # remote_dump_dir: path inside the guest where the dump will be placed.
  # Created automatically by oubliette. Default: /tmp/oubliette-dump.
  remote_dump_dir: /var/run/oubliette/dump

  # remote_criu_path: path to the criu binary inside the guest. Default: "criu".
  remote_criu_path: /usr/sbin/criu
```

## Migration sequence

1. **VM lookup** — `virsh domstate <vm>` confirms the domain is running.
2. **IP discovery** — `virsh domifaddr <vm>` resolves the guest's primary IPv4 address.
3. **Preflight checks** (source process not touched until all pass):
   - `/proc/<pid>/status` is readable.
   - Process CWD exists at the same path on the guest (`ssh test -d <cwd>`).
   - `criu` is executable on the guest (`ssh test -x <remote_criu_path>`).
4. **CRIU dump** — process is checkpointed on the host; it is left in a stopped state.
5. **Transfer** — dump directory is copied to the guest via `scp -r`.
6. **Remote restore** — `criu restore --detach` runs on the guest over SSH; CRIU
   hands off the process tree and exits so SSH does not block.
7. **Kill source** — the stopped source process is sent `SIGKILL`.

## Testing

Unit tests (no CRIU or VM required):

```sh
go test ./...
```

Integration tests (require CRIU ≥ 3.15 and a running libvirt domain):

```sh
go test -tags integration ./...
```

## Known limitations (v0.0.1)

- No PTY/terminal handoff. The restored process's stdio is detached inside the
  guest; interactive shells need manual PTY re-attachment after migration.
- Paths passed via config must not contain spaces or shell metacharacters.
- Network connections open at dump time are not re-established in the guest
  (CRIU can restore TCP connections but this is not yet wired up).
- Only the first IPv4 address from `virsh domifaddr` is used.
