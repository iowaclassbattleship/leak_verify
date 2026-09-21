# Provenance

Dataset marking and attribution. Mark a table per recipient, and identify which
copy a recovered file came from.

Runs on one host as a local web app. All state is in memory, and nothing leaves
the machine.

Run from the repository root:

```sh
go run ./provenance                          # http://127.0.0.1:8081
go run ./provenance -addr 127.0.0.1:9000     # other port
go test ./provenance/...                     # robustness tests
```

The marking key is random per process, so restarting clears all state. The
frontend is served from `provenance/web/`, so editing an HTML, CSS or JS file only needs a
browser refresh.

## The app

- **Mark:** the head of the loaded table with a checkbox per measure. Seven are
  offered: canary rows, low-order bits and a dummy column, which are the
  classic three, plus allocation (a keyed slice of rows withheld), tuple
  ordering (adjacent rows swapped), free choices (the same number written with
  another decimal place), noise as a carrier (the perturbation a privacy
  pipeline already applies, seeded per recipient) and redaction as a carrier
  (names replaced by a keyed pseudonym, which the redaction step has to emit
  either way). The last five touch no value that means anything, which is what
  makes them usable on data that must stay analytically intact. Each states its
  own cost, including where it cannot be applied at all,. Every carrier's rate is tunable through
  `tabular.Params`, which the API accepts but the interface mostly does not
  expose. The one control that is on screen is which decimal place low-order
  bits perturb, because the size of that change is the question a data owner
  actually asks. Toggling a
  measure re-marks the data and shows the result in place: changed cells with
  the previous value struck through, added rows, added columns. Below the table
  the impact is counted over the whole copy, so the cost of each measure is a
  number rather than a claim.
- **Verify:** drop a recovered CSV to check it against the copy the Mark tab is
  configured to produce. Each carrier reports separately, with the stage it
  belongs to (exact or fuzzy matching) and what it found, then the verdict
  pools them. A file that was never issued here is reported as such rather than
  being forced into a match.

The Mark tab records the copy it is configured to produce, so Verify always has
something to check against. Every carrier is read back, each by its own stage:
exact fingerprint, canary rows and allocation by exact matching, and low-order
bits, noise, free choices, tuple ordering, redaction and the dummy column by
fuzzy matching against the source.

## Column roles

Roles are detected from the data and confirmed by the user before marking:

- **Identifier:** a text or whole-number column whose values are distinct. Marks
  are keyed to it, and it matches a recovered row back to the source. With no
  identifier, rows are recognised by the columns marking leaves alone.
- **Tolerant:** a decimal column with at least three decimals, or a timestamp
  with fractional seconds. Only these carry low-order-bit marks, so nothing
  without a stated tolerance is ever altered.

## Techniques

| Technique | How it works |
|---|---|
| Canary rows | About 0.5% synthetic rows per recipient, built from a real row with fresh identifying values so every column stays plausible. |
| Low-order-bit mark | For each tolerant cell, a keyed HMAC of the row's identifying value decides whether the cell carries a bit, which codeword bit, and its mask. The value's last digit holds the bit. |
| Dummy column | `ref_code` = `BR-<id XOR keyed pad>-<check>`. Easy to drop, and included only as the weakest layer. |

Detection runs in two stages: **exact matching** (SHA-256 row fingerprints
against each recipient's regenerated copy, plus canary lookup) and **fuzzy
matching** (majority vote per codeword bit, then error-correcting decode, plus
the dummy column).

## Codec (`../common/codec`)

- Each 16-bit mark ID becomes four nibbles, each encoded with extended
  Hamming(8,4). The resulting 32 bits are XORed with a keyed whitening mask.
- New IDs keep a codeword distance of at least 12 from every existing ID.
- Decoding is soft decision per nibble; candidates from the issuance log are
  ranked by bit agreement.
- A copy is identified when the best candidate is unique and the chance that an
  unrelated file matches this well is at most 1 in 1,000.

## Limits

- Marks are keyed to the identifying column. If none survives, rows are matched
  by another unique column or by the columns marking leaves alone.
- Rounding below the marked digit destroys that layer by design.
- Canary rows identify a recipient only if a sampled leak contains one.
- Canary rows are built from real rows with fresh identifying values, so they
  are plausible column by column but may combine attributes that rarely occur
  together.
- Merged copies from several recipients are flagged, not resolved.
- Covert marking is a technical property. Whether recipients are told is a legal
  and policy decision.
