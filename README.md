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

## Development and testing VMs (`vm/debian/`)

Debian-only for now (the scripts assume `apt`, `virsh`, `virt-install`).
They manage two independent libvirt domains on your L0 host:

| Domain | Scripts | Role |
|--------|---------|------|
| **devhost** (L1) | `vm/debian/devhost/` | A plain Debian dev box for building and running oubliette itself. Project source is shared in over 9p at `~/src`. It does not run CRIU or nested virtualization — it's just somewhere to write and build code on Linux. |
| **shadow VM** ("falltrap") | `vm/debian/` | The actual migration target used to exercise CRIU dump/restore round-trips. Provisioned with CRIU, a virtiofs shared directory (checkpoint transport), and a vsock device (reserved for a future PTY/control channel). |

You don't need one to use the other, but a full round-trip test typically
uses devhost to build and drive oubliette, with the shadow VM as the
migration target.

### First, set up the host machine

```sh
./vm/debian/host-setup.sh
```

Installs QEMU/KVM, libvirt, virt-install, CRIU, and genisoimage via `apt`,
adds your user to the `libvirt` group, and marks the host as ready so the
per-VM scripts below can confirm it. **Contains network requests**
(package installation). Run this once per host — for the shadow VM path
below, which runs directly on L0. `devhost/create.sh` runs this same
script for you automatically (see next section), just *inside* L1
instead.

### devhost — for development

```sh
./vm/debian/devhost/create.sh
```

One-time. Contains network requests: fetches the Debian cloud image if
not already cached (on L0), then, once the L1 VM boots, pipes
`../host-setup.sh` into it over SSH and runs it there — so L1 ends up
with its own qemu/libvirt/criu, ready to host a nested L2 shadow VM.
Refuses to run if the domain already exists.

Every session:

```sh
./vm/debian/devhost/start.sh
./vm/debian/devhost/healthcheck.sh
./vm/debian/devhost/shell.sh
```

`start.sh` boots the domain and blocks until SSH answers, printing the
domain name on stdout (progress goes to stderr) so it can be captured by
other tooling. `healthcheck.sh` confirms libvirt is reachable, the domain
is defined, and the `~/src` share is mounted. `shell.sh` drops into an
interactive shell — pass `-- <cmd>` for a one-off command, or pipe a
script on stdin.

When done:

```sh
./vm/debian/devhost/stop.sh
```

### shadow VM — for CRIU round-trips

```sh
./vm/debian/host-setup.sh   # if not already done above
./vm/debian/create.sh       # one-time; network request for the cloud image
```

Every session:

```sh
./vm/debian/start.sh          # prints the domain name on stdout
./vm/debian/healthcheck.sh    # confirms CRIU-ready on both host and guest
./vm/debian/shell.sh          # drop into the guest to poke around
```

`healthcheck.sh` checks both sides of the CRIU round-trip: `virsh`,
`virt-install`, `criu`, and `qemu-img` on the host, plus `libvirtd`
running and the domain defined; then, if the guest is up, its kernel
version, `criu check`, the virtiofs shared mount, and the vsock device.

When done:

```sh
./vm/debian/stop.sh
```

### Clean rebuilds

Both VMs have a `destroy.sh` for tearing down the domain and its
per-domain artifacts (overlay disk, cloud-init seed) while keeping the
downloaded base cloud image and SSH key, so a subsequent `create.sh` is
cheap:

```sh
./vm/debian/devhost/destroy.sh [--yes]
./vm/debian/destroy.sh [--yes]
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

Bring up the shadow VM first (see [Development and testing
VMs](#development-and-testing-vms-vmdebian) above) and run
`./vm/debian/healthcheck.sh` to confirm it's CRIU-ready before running
these.

## Experimental branch flow
 1. Start the devhost machine : `./vm/debian/devhost/create.sh && ./vm/debian/devhost/start.sh`
 2. Start the guest machine : `./vm/debian/devhost/shell.sh -- ./src/vm/debian/create.sh && ./src/vm/debian/start.sh`
 3. Run process A in terminal A : `./vm/debian/devhost/shell.sh`
 4. From terminal A build oubliette : `cd src && ./build.sh`
 5. From terminal A ensure fifo is ok : `./src/vm/debian/shell.sh -- 'sudo -u falltrap sh -c "rm -f /run/user/1000/fish_universal_variables.notifier; mkfifo -m 600 /run/user/1000/fish_universal_variables.notifier"'`
 6. Run process B in terminal B : `./vm/debian/devhost/shell.sh`
 7. From terminal B Get pid process B : `echo $fish_pid`
 8. From terminal A ensure we are in a host vm : `sudo cat /root/oubliette_status.txt`
 9. From terminal A sync and steal process : `./vm/debian/sync-session.sh && sudo ../oubliette --pid [PID_TO_STEAL]`
  - Terminal B should be killed quickly
 10. From terminal A : press enter
 11. From terminal A ensure we are in a guest vm : `sudo cat /root/oubliette_status.txt`
In the end we know we have successful stolen process and we are inside of a guest vm because `/root/oubliette_status.txt` does not exist. Whereas it has "installation ok" in a host (devhost or real host).
