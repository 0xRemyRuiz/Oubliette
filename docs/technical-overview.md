## Introduction
First, the three channels they use — worth holding in mind, because both programs are built around them:
- Checkpoint transport — a virtiofs shared directory. The host writes CRIU's checkpoint images into it; the guest reads them. Bulk, one-way.
- Live terminal + control — an AF_VSOCK stream between the host and the guest helper. Carries the pty byte stream and window-size updates (framed by internal/ptymux).
- Guest command execution — the QEMU guest agent (internal/qga). The host uses it to run preflight probes on the guest and to launch the restore helper.

## Program 1 — oubliette (runs on the host / L1)
cmd/oubliette/main.go dispatches two subcommands. Here's migrate --pid (the engine test), step by step:

1. Startup — parse flags, load oubliette.yaml (internal/config), set up structured logging.
2. Verify the VM — internal/vm calls virsh to confirm the shadow domain is running.
3. Preflight (internal/preflight) — nothing touches the source until all pass:
  - the target PID exists and /proc/<pid>/status is readable;
  - its CWD exists at the same path on the guest (probed via the guest agent);
  - every real file the tree holds open exists on the guest (the check I just added — catches things like the fish FIFO before the dump);
  - criu is executable on the guest; the guest has a vsock device.
4. Acquire a freezer cgroup (internal/coherence) — create a cgroup v2 node and move the target's whole process tree into it (Populate: freeze, pull in stragglers while staying frozen so a fork-heavy tree can't escape).
5. Gate to a coherent instant (gatedDump) — loop: freeze the tree → read every process's open fds → if any points into /proc//sys (unreproducible in the guest), thaw, wait, retry; when the snapshot is clean, stop with the tree frozen.
6. Checkpoint (internal/criu) — thaw and run criu dump -t <pid> (with --shell-job, --ghost-limit, --file-locks), which snapshots the whole subtree into the local dump dir and kills the source tree. If the dump loses the thaw/seize race it re-gates.
7. Transfer (internal/transfer) — copy the checkpoint images into the virtiofs shared dir, and stage the oubliette-restorehelper binary there too.
8. Launch the guest helper (internal/qga) — via the guest agent, start oubliette-restorehelper in the shadow VM (backgrounded with setsid nohup).
9. Connect — dial the helper's vsock port, retrying. The helper only starts listening after a successful restore, so a successful dial is the restore confirmation.
10. Finalize — SIGKILL the source (already dead from the dump; ESRCH expected), and return the connected vsock channel.
11. Attach — put oubliette's own terminal into raw mode and rever vsock (internal/ptymux), forwarding resizes. This is why
migrate --pid is "stealing": the session lands in oubliette's

The serve (broker) path reuses steps 2–10 but wraps them for s
 - It listens on TCP and gives each client a shell on a pty it wned directly into a freezer cgroup at birth (CgroupFD) as a
session leader.
 - It relays client↔shell while scanning the client's input forits for an external containment request on the control socket,internal/control).- When the trap springs, it calls the same migration engine (Mirth cgroup), then re-points the client's existing TCPconnection at the guest copy. The client never sees a break —

## Program 2 — oubliette-restorehelper (runs in the guest / L2)
cmd/oubliette-restorehelper/main.go parses --dump-dir, --criu-rnal/restorehelper.Run:
 1. Allocate a fresh pty in the guest (internal/pty → new masteal can't cross the boundary, so a new one is built here.
 2. Restore onto it — run criu restore --shell-job --restore-den/out/err and Setsid+Setctty, so CRIU's session takes the slaveas its controlling terminal and the restored shell inherits itrestore.log in the dump dir (visible from the host viavirtiofs).
 3. Wait for restore to complete; CRIU detaches and hands off t
 4. Start listening on the vsock port — only now, so the host'store.
 5. Accept the host's single connection.
 6. Bridge — relay the pty master ↔ vsock (internal/ptymux), apupdates to the master. Runs until the session ends.
The net effect: the same processes resume in the guest at the nal reconstructed under them, stitched back to whoever's on thefar end (operator terminal for migrate, original client for se