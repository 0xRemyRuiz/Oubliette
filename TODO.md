# Current steps TODO

## Setting things up
 [X] Dev env is successfully set up
 [X] Step 1 (falltrap mechanism)
 [X] Step 2 (wathdog process)
 [ ] Step 3 (collector feeding)
 [ ] Step 4 (paramterized config)

## Typical usage profiling
 [X] Typical network topology
 [X] Recommended machine profile (justified)
 [ ] Detail the recommendation against critical assets

## Working PoC
 [ ] Basic linPEAS script triggers the trap seamlessly
 [ ] Collector gets monitoring infos

# TODO list for future developments

## A. Finish & harden the core PoC
- [ ] Fix the job-control gap properly: preflight should warn/refuse when the target isn't a session leader (the tcsetpgrp failure), so it's caught upfront.
- [ ] Implement seamless migrate --pid — bridge the restored session back to the target's own pts (the "seamless falling" plan in NOTES) instead of operator-attach.
- [ ] Bring the L1→L2 stack up and validate end to end: confirm a real mid-linpeas migration restores; measure the freeze window (dump+transfer+restore); validate adopt_freeze (Design B --freeze-cgroup) against the guest CRIU.
- [ ] Auto-staging / "coherence loop": checkRemoteOpenFiles finds missing guest files — have it (optionally) copy them over (script, FIFO, locale archive) instead of only failing loud.
- [ ] Tighten the freeze window: dump straight into the virtiofs dir (skip the local→shared copy), pre-launch the restore helper, shorten the dial loop.
- [ ] Refine DefaultClassifier against real CRIU (it's conservative — some /proc//sys fds are actually restorable).
- [ ] Revisit the half-migration window (dump kills the source before restore is confirmed) — decide/document the acceptable risk or add a rollback.
- [ ] Surface CRIU failures better (parse dump.log/restore.log and report the concrete reason).

## B. External detection & triggering (Suricata)
- [ ] Make the Suricata trigger reliable (NOTES says it's flaky/random) — solid rules for the attacker behaviors you care about.
- [ ] Wire Suricata alerts → the control socket, and map a network flow to the right session/PID to contain (ContainRemote/ContainPID already exist).
- [ ] Pluggable trigger interface so EDR / honeytokens / canary files can also fire the trap (product-overview names these as complements).
- [ ] Tune false-positive handling (Risk3): don't trap legitimate users.

## C. Productionization
- [ ] Collapse L0/L1 into the real "protected host + shadow VM" model; drop Debian-only assumptions in the scripts.
- [ ] Managed shadow-VM lifecycle: a pool of pre-warmed guests, provisioning, health, teardown/reset after each trap.
- [ ] Filesystem-coherence at scale: base-image "aging" engine (synthetic-but-believable guest FS), automated path matching.
- [ ] Concurrency: multiple simultaneous sessions/traps.
- [ ] Isolation & network segmentation around the shadow VM (Risk4) — treat it as hostile.
- [ ] Attacker-behavior recording inside the shadow VM (the intelligence payoff) + defender alerting when a trap springs.
- [ ] Packaging & ops: systemd unit, .deb, config/secrets management, root-privilege hardening (this thing does ptrace/CRIU as root — real attack surface).

## D. CI/CD & engineering hygiene
- [ ] CI: gofmt check, go vet, go test ./... (hermetic), golangci-lint (config already committed) on every push.
- [ ] A gated integration stage on a runner with cgroup v2 delegation / CRIU / nested KVM to exercise the live-cgroup tests and a real L1→L2 migration (the //go:build integration tests).
- [ ] Coverage reporting; release automation (versioned binaries, changelog).
- [ ] verify/smoke script that stands up the stack and runs one migration as a gate.

## E. Things you may be missing
- [ ] Anti-detection hardening: make the shadow VM not look like a VM/trap (a savvy attacker will probe — Risk4).
- [ ] Deception-believability testing: does the attacker notice the pause, the FS, the environment?
- [ ] Session reversal/extraction & clean termination (migration is one-way today).
- [ ] Security review of oubliette itself (a root daemon fronting untrusted input).
- [ ] Threat model + deployment/authorization guidance (deploying deception on real systems has legal/ethical/operational constraints).
- [ ] Metrics: trap rate, dwell time, false-positive rate — the numbers that justify the tool.
