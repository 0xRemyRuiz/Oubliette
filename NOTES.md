Make sure qemu has the right permissions (WRONG)
------------------------------------------------
 - setfacl -R -m u:libvirt-qemu:rwx $(pwd)
 - setfacl -R -d -m u:libvirt-qemu:rwx $(pwd)
 - sudo chown $(whoami):$(whoami) -R $(pwd)

At one point my image wasn't loading from the internet anymore so I changed it and complained about it to claude. It did some serious checking tasks, again and again, said to me I got the wrong config and changed it. I've got another problem about having the rights to build the oubliette command from the L1 vm. It did a lot of half useful checking tasks to final tell me "hm, it's an ACL problem obviously" and proposed me to setup ACLs. It worked but now I was having trouble getting qemu to enlist my L2 vm to the network. It worked again heavily to produce fixes on fixes without the situation improving at all. It was starting to spiral to infinity. 50k tokens later, turns out the best fix was to build my command a folder above and voila ! Think with the AI, don't make it think for you...

Profile of the machine
----------------------
My impression is that the machine should be a medium to low critical asset. It is not designed for critical assets since we cannot warranty for sure the integrity of the machine. First because we let the attacker run commands before trapping him. Second because we cannot be sure (and cannot approach the certainty of a behavior based analysis) that we detect the wrongful action. Third because we cannot be sure we get only attackers. Fourth because we cannot be 100% sure the attacker stays in the trap dungeon.

Notions to look about
---------------------
 - login linger
 - 9p (this is the shared mechanism used to sync data from host with devhost)

Plan to switch from stealing the process to seemless falling generated
----------------------------------------------------------------------
 1. Capture the target's terminal before touching it (internal/migrate)
	- targetTTY(pid) (string, error): readlink /proc/<pid>/fd/0, require it matches /dev/pts/*. If not → abort with a clear error ("target PID has no controlling terminal; oubliette migrates interactive tty sessions"). Runs in preflight, before the dump, so we fail early.
 2. Bridge to the target's pts, not oubliette's stdin/stdout (internal/migrate)
	- Replace terminalConn (which wraps os.Stdin/os.Stdout) with one that opens the captured /dev/pts/N (O_RDWR|O_NOCTTY), saves its termios, sets raw, and restores on close. The raw-mode logic I just added moves over almost verbatim. oubliette's own terminal (B) keeps showing logs; the live session lands back in terminal A.
 3. Live resize — with an important wrinkle
	- oubliette is not in terminal A's foreground process group (it runs in terminal B), so the kernel will not send it SIGWINCH when the user resizes terminal A. So instead of a signal handler, a goroutine polls TIOCGWINSZ on the target pts (~200 ms) and forwards the size only when it changes. Initial size is sent immediately on attach. Slight latency, but robust and correct for an out-of-band observer.
4. Multiplex data + winsize over the one vsock (new internal/ptymux, replaces internal/ptybridge)
	- A tiny framed protocol: [1 byte type][2 byte len][payload], types DATA and WINSIZE (payload = rows,cols as two uint16).
	- Relay{ local, conn io.ReadWriteCloser; onWinsize func(rows,cols uint16) } with Run(ctx) error and SendWinsize(rows,cols) error (conn writes mutex-guarded). One symmetric type serves both ends:
	  - Host: onWinsize=nil (never receives), calls SendWinsize from the poll loop.
	  - Guest helper: onWinsize= apply TIOCSWINSZ to the pty master; never sends.
	- Helper keeps the 80×24 seed as the pre-first-frame default; the host's initial WINSIZE corrects it immediately.

Files: internal/migrate/migrate.go (capture tty, open pts, poll loop, use Relay), internal/restorehelper/restorehelper.go (use Relay with a winsize handler), new internal/ptymux/{ptymux.go,ptymux_test.go}, remove internal/ptybridge/. Both binaries rebuilt.

One trade-off to flag: I'm folding winsize into the existing connection via light framing (every pty byte now passes through frame encode/decode). The alternative is a second vsock control connection that leaves tched and carries only fixed 4-byte winsize messages. The framedapproach is cleaner (one connection, no dual-teardown edge cases); the two-connection approach is lower-risk to the data path that just started working. I
lean framed and will cover it with tests — but say the word ifath raw.

Base for prez
-------------
I need a sobre presentation like a technical talk, something like in DEFCON talks or other tech talks. I will be presenting a research project using agentic working. My research project is some sort of a falltrap mechanism in linux. It is not to replace honyepot but to complement those.

Oubliette

A Linux deception system that transparently drops an intruder's live terminal session into a shadow virtual machine. The attacker keeps typing; the floor was never real. Named for the medieval trapdoor dungeon — a cell where prisoners are dropped from above and forgotten — mirroring the mechanism: the session is silently relocated and the real system forgets it was ever there.



The approach

The idea was to not only research into a field no one ever has, but to do it extensively with the help of AI, here with claude since it seems to be the most advanced coding-wise. The goal was not to necessarily have a full working Poc but to see how far we can go in such a short time dedicated to the project.

The Problem

Once an attacker has a shell on a machine, every second they spend on the real filesystem is exposure. Traditional honeypots try to lure them into an obviously fake box. The harder, more valuable goal: let them land on a real machine, then move them out from under their own feet — without them noticing the transition.



The Paths We Considered

Selective data classification — tag sensitive files, fake only those. Rejected: unbounded, tedious, seams show.

Honeypot fusion (Cowrie / OpenCanary) — bolt on an existing honeypot. Rejected: emulates a service surface with a fake filesystem — its weakest, most-fingerprinted layer.

Real shell over a synthetic tree — strong, but solves where the connection lands, not where the process runs.

Base-image aging engine — overlay synthetic identity on a real image. Valuable, but a separate concern from migration.

CRIU live process migration into KVM — checkpoint the running process, restore it inside the shadow VM. The only approach that relocates execution itself.



The Path We Chose

CRIU-based transparent session migration into a same-host KVM shadow VM.

Why it won:

Satisfies the core rule — the attacker must not feel they fell into a trap. The process genuinely continues; nothing is emulated.

Real bash, real tools, real shell inside the guest — undetectable at the shell level because everything is real, only the content is synthetic.

Same-host deployment keeps the switch fast, which is what "no notice" requires.



Development Process — Three Phases

Phase 1 — Environment & PoC: making a process fall. Stand up the nested research environment and prove a running process can be transparently migrated from host into the shadow KVM. This is the foundational mechanism — the falltrap itself.

Phase 2 — The watchdog: triggering the fall. Build the detection layer that decides when to spring the trap, and fires the migration automatically instead of by manual command.

Phase 3 — Full automation for the fictitious hacker. End-to-end automated flow, validated against the LinPEAS recon script as a stand-in attacker. Key constraint: the attacker's session must never break — the script runs to completion, uninterrupted, straight through the migration.



Environment Architecture

In the end the context is simple:

L1 — Host — this is the machine hosting the falltrap mechanism

L2 — Shadow VM — the oubliette itself, 

Restricted to Debian for the research phase; the design isn't Debian-bound.



Development Architecture

Nested virtualization keeps the daily-driver machine clean and the experiment reproducible:

L0 — Developer Host — development machine, editor, source of truth.

L1 — Debian devhost VM — the "host" in all tooling: runs the engine, libvirt, CRIU. Source shared in from L0 over 9p, no git round-trips.

L2 — Debian shadow VM — the falltrap target the intruder is dropped into.



Where This Belongs — Target Machine Profile

Oubliette suits medium-to-low criticality assets. It is deliberately not for critical systems, because the design cannot guarantee machine integrity. It trades certainty for intelligence and deception value.



Why Not Critical Assets — Four Honest Limits

The attacker acts before the trap. We let them run commands on the real machine before migrating them — that window is real exposure.

Detection is imperfect. No trigger, however tuned, approaches the certainty of true behavior-based analysis; wrongful actions can be missed.

Not everyone caught is an attacker. We cannot be sure the trigger only fires on genuine intruders — false positives are possible.

Containment isn't absolute. We cannot be 100% certain the attacker stays inside the shadow dungeon once dropped.

These aren't bugs to be fixed later — they're structural properties. Deploy where deception value outweighs the residual risk.