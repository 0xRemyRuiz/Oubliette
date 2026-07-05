# Oubliette — Product Documentation

*Transparent session migration for deception-based defense*

---

## 1. Description and Goal

### What Oubliette is

Oubliette is a Linux deception system that transparently migrates an intruder's
live terminal session from a real host machine into an isolated shadow virtual
machine (KVM). The intruder continues typing into the same session; the
execution context is silently relocated beneath them. From their perspective,
nothing changed — the shell responds, commands run, the filesystem is there.
In reality, they are no longer on the protected machine.

The name is taken from the medieval *oubliette*: a dungeon accessible only
through a trapdoor in the ceiling, from the French *oublier*, "to forget." A
prisoner was dropped in and forgotten. The metaphor is precise — the intruder's
session is dropped into a lower space and the real system ceases to know they
were ever there.

### The problem it addresses

Once an attacker obtains an interactive shell on a machine, every moment they
spend on the real filesystem is exposure. They can read, exfiltrate, and pivot.
Conventional deception tools attempt to lure attackers into an obviously
synthetic environment up front, but a capable adversary detects the artifice
quickly and disengages.

Oubliette inverts the sequence. The attacker lands on what is — or convincingly
appears to be — a genuine system, and is then relocated *out from under their
own feet* into a controlled environment, without a perceptible transition. The
defensive value is twofold: the attacker's real access is curtailed, and their
subsequent behaviour can be observed inside a contained space.

### Design goal

The central, non-negotiable design goal is that **the transition must not be
perceptible to the intruder**. Every architectural decision follows from this:
the process is genuinely migrated rather than emulated, the shadow environment
runs a real shell with real tools, and the switch is executed on the same host
to keep it fast. A deception the attacker can feel is worse than no deception at
all, because it confirms to them that they have been detected.

---

## 2. Machine Profile Recommendation

Oubliette is suited to **medium-to-low criticality assets**. It is
deliberately **not recommended for critical or high-assurance systems**.

This recommendation is structural, not a limitation to be engineered away in a
later release. The system trades guaranteed integrity for intelligence and
deception value, and that trade is only appropriate where the residual risk is
acceptable.

### Suitable deployments

- Internet-facing systems of moderate value where intrusion attempts are
  expected and observation of attacker behaviour is useful.
- Secondary or peripheral infrastructure where the primary defensive objective
  is early detection and intelligence-gathering rather than absolute protection.
- Environments where an intruder's engagement time and technique are themselves
  valuable signals to the defender.
- Assets whose compromise would be inconvenient but not catastrophic, and which
  can be rebuilt cleanly if integrity is ever in question.

### Unsuitable deployments

- Systems holding regulated, irreplaceable, or highly sensitive data whose
  exposure — even briefly, even partially — is unacceptable.
- Critical operational systems whose integrity must be guaranteed rather than
  probabilistically defended.
- Any context where the four residual risks described in Section 4 cannot be
  tolerated.

The guiding principle: deploy Oubliette where the **value of deception and
intelligence outweighs the residual uncertainty about machine integrity**. Where
integrity must be certain, Oubliette is the wrong tool.

---

### Key infrastructure requirements

- **Nested virtualization** enabled on L0 (`kvm_intel nested=1` or
  `kvm_amd nested=1`), and L1 launched with CPU passthrough so that L2 can use
  KVM acceleration.
- **CRIU** (Checkpoint/Restore In Userspace) installed and kernel-capable on
  both sides of the migration boundary.
- **A shared transport** (virtiofs or 9p) to carry the process checkpoint from
  host to shadow VM.
- **A control channel** (vsock) between host and shadow VM for the session
  bridging that keeps the terminal connected across the switch.
- **libvirt** managing the VM lifecycle, accessed through the system connection
  scope (`qemu:///system`).

The restriction to Debian applies to the research phase only; the design is not
Debian-bound. In production the L0/L1 distinction may collapse — the "host" is
simply whatever protected machine runs the engine, and the shadow VM runs on it.

---

## 3. Risk Assessment

Oubliette provides deception and intelligence value but **cannot guarantee the
integrity of the protected machine**. Four residual risks are structural
properties of the approach. They are presented here honestly because
understanding them is a precondition for correct deployment.

### Risk 1 — The attacker acts before the trap springs

Oubliette does not prevent initial access. By design, the intruder is permitted
to obtain a shell and issue commands on the real machine for some interval
before migration occurs. That interval is genuine exposure: anything the
attacker reads, copies, or alters in that window happens on the real asset.
Reducing the window improves the outcome but never eliminates it.

### Risk 2 — Detection is imperfect

The decision to spring the trap depends on a trigger (Phase 2, the watchdog).
No trigger, however carefully tuned, approaches the certainty of comprehensive
behaviour-based analysis. Malicious actions can be missed entirely, or
recognized only after significant activity has already occurred. A missed
detection means the attacker is never migrated and operates on the real machine
unimpeded.

### Risk 3 — Not everyone caught is an attacker

The trigger cannot be assumed to fire only on genuine intruders. Legitimate
users or automated processes whose behaviour resembles the trigger conditions
may be migrated into the shadow environment. Beyond the operational disruption
of trapping a legitimate session, false positives carry their own cost:
they may reveal the existence of the deception system to observers.

### Risk 4 — Containment is not absolute

Once an intruder is inside the shadow VM, there is no absolute guarantee they
remain there. Virtual-machine isolation is strong but not unbreakable; a
sufficiently capable attacker may detect the environment, or in the worst case
attempt to escape it. The shadow VM should therefore itself be treated as
potentially hostile and isolated accordingly, never as a trusted zone.

### Summary of residual risk

| Risk | Consequence | Nature |
|------|-------------|--------|
| Pre-trap action | Real exposure before migration | Structural — cannot be eliminated |
| Imperfect detection | Attackers missed; never trapped | Inherent to trigger-based detection |
| False positives | Legitimate sessions trapped; deception revealed | Inherent to behavioural triggers |
| Imperfect containment | Attacker may detect or escape the shadow VM | Inherent to VM isolation limits |

Taken together, these four risks are the reason Oubliette is positioned for
medium-to-low criticality assets. They do not undermine its value where that
value is correctly understood: deception, delay, and intelligence — not
guaranteed protection.

---

## 4. Conclusion — Complementarity with Other Solutions

Oubliette is not a replacement for existing defensive tooling. It occupies a
specific niche — transparent, mid-session relocation of an active intruder — and
is most effective as one layer within a defence-in-depth posture. Its
limitations in one area are precisely where other tools are strong, and vice
versa.

### With honeypots

Traditional honeypots (such as Cowrie or OpenCanary) present a synthetic service
surface designed to attract and log attackers from the moment of connection.
Their weakness is that the environment is fake from the outset and can be
fingerprinted. Oubliette's weakness is the opposite: it must let the attacker
onto a real machine first. The two are complementary. A honeypot draws and
catalogues opportunistic and automated threats at the perimeter; Oubliette
handles the more capable adversary who has already gained a foothold on a real
system, relocating them once they act. A honeypot answers "who is knocking";
Oubliette answers "what does someone do once inside."

### With behaviour-based detection and EDR

Oubliette's second structural risk is imperfect detection. This is exactly the
domain of mature behavioural analysis and endpoint detection and response
tooling, which can bring far richer signals to the decision of *when* to spring
the trap. Rather than competing, a strong detection layer can serve as
Oubliette's trigger, and Oubliette can serve as an active *response* to that
detection — moving the intruder rather than merely alerting on them. Detection
tools decide; Oubliette acts.

### With honeytokens and canary files

Honeytokens — deliberately planted credentials or files that alert when touched
— address Oubliette's first risk (action before the trap) and its third (telling
attackers from legitimate users). A honeytoken accessed inside the pre-migration
window is a high-confidence signal of malicious intent, and can itself be the
trigger that fires the migration. Placed inside the shadow VM, honeytokens
further help confirm that a trapped session is behaving maliciously.

### With strong isolation and network segmentation

Oubliette's fourth risk — imperfect containment — is mitigated by treating the
shadow VM as a hostile zone and applying rigorous network segmentation around
it. The deception buys time and intelligence; the surrounding isolation
architecture ensures that a contained attacker, even one who detects the trap,
cannot use it as a stepping stone. Oubliette relocates the threat; segmentation
ensures the relocation is meaningful.

### Closing position

Oubliette contributes something distinct to a layered defence: the ability to
transparently move an active adversary out of a real environment and into a
controlled one, without alerting them. It does not guarantee integrity, detect
perfectly, or contain absolutely — and it does not need to, because those
guarantees are the province of the tools it sits alongside. Deployed on
appropriately-scoped assets and integrated with honeypots at the perimeter,
behavioural detection as its trigger, honeytokens as high-confidence signals,
and strong isolation around the shadow environment, Oubliette turns the moment
of intrusion from a pure loss into an opportunity for observation and control.