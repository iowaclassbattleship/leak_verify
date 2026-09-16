# Leak Attribution Platform — Project Brief

A demonstrator for a data-leak attribution system with two independent modules.
Everything runs on client infrastructure; no data leaves the environment.

## Module A — Dataset Attribution (structured/tabular data)

Marks a source database so a leaked copy can be traced back to the recipient it
was issued to, and detects the mark in a recovered artifact.

**Embedding strategies (offer all three, layered):**
- **Canary rows** — synthetic records indistinguishable from real ones, inserted
  at issuance. Early-warning tripwire; coarse attribution. Does not alter real data.
- **Low-order-bit masking** — per-recipient bit pattern spread across the
  least-significant, noise-tolerant part of real values (trailing decimals,
  sub-second timestamps). Fine-grained, survives sampling via error-correcting
  codes. Only applied to fields with a stated tolerance.
- **Dummy column** — recipient-encoding column disguised as legitimate data.
  Weakest (droppable, obvious under comparison); include only where threat model allows.

**Comparison pipeline:**
- Exact matching — direct / minimally altered copies (hash fingerprint patterns).
- Fuzzy matching — transformed, sampled, or partially obfuscated data (recover the
  error-corrected code).

**Demo interaction:** mark one synthetic table three ways → "leak simulator"
applies attacks (drop columns / sample rows / round values) → detector shows which
techniques still attribute correctly after each attack.

## Module B — Document Attribution (PDFs)

At distribution, each recipient gets an identical-looking copy carrying an
invisible, recipient-unique identifier. On a leak, read the surviving mark and
look it up against the issuance log.

**Embedding layers (combine for robustness):**
- Text-layout micro-perturbations (spacing / glyph position) — survives print+scan.
- Linguistic/content variation — survives copy-paste; changes text (not for verbatim docs).
- Invisible object-layer marks — survives screenshot / photo of screen.
- Metadata/structural tags — weak supplementary layer only; trivially stripped.

**Issuance log:** maps mark ID → recipient → distribution timestamp, within a
privilege hierarchy.

**Demo interaction:** upload a PDF → issue N recipient copies → optionally attack
one (screenshot / print+scan / re-save) → detector decodes the mark and names the recipient.

## Cross-cutting notes
- On-prem only; no artifact leaves the environment.
- Layer techniques + error-correcting codes for robustness; no single method
  survives every transformation — be explicit about where each breaks.
- Covert marking is a technical property; whether recipients are put on notice is a
  legal/policy decision left to the client.

## Demo scope
Two clickable prototypes (one per module) to show the workflow end to end. Throwaway
demonstrator code, not the production build. Prioritize a convincing, honest
mark-then-attack-then-detect loop over completeness.