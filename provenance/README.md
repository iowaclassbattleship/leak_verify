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
  own cost, and a measure the table has no eligible column for is disabled
  with the reason before anything is issued. Every carrier's rate is tunable through
  `tabular.Params`, which the API accepts but the interface mostly does not
  expose. The one control that is on screen is which decimal place low-order
  bits perturb, because the size of that change is the question a data owner
  actually asks. Toggling a
  measure re-marks the data and shows the result in place: changed cells with
  the previous value struck through, added rows, added columns. Below the table
  the impact is counted over the whole copy, so the cost of each measure is a
  number rather than a claim.
- **Issue to a recipient:** name the people or organisations a copy is going
  to, what it is for, and which group they belong to, and issue one marked copy
  each, with the measures chosen above. The issuance log is what turns a
  decoded mark back into a name, and it survives a restart, together with the
  column roles each copy was marked under.
- **Verify:** drop a recovered CSV to check it against the issued copies. Each
  carrier reports separately, with the stage it belongs to (exact or fuzzy
  matching) and what it found, then the verdict pools them and names the
  recipient, the purpose and the issue date. Rows from several recipients'
  copies give a *Merged copies detected* headline listing each of them. A file
  whose rows are all in the unmarked source says so, and a file that was never
  issued here is reported as such rather than being forced into a match.
- **Log:** every copy issued from any table, scoped to the viewer the way
  Custodial's log is: a viewer sees their own group, and only Data Governance
  sees the mark IDs.

Verify reads a file against every issued copy of every table loaded in the
session, whichever table the Mark tab shows: each table's copies are
regenerated under the roles they were issued with, and the strongest reading
wins. The Mark tab's unassigned preview copy is never a candidate, since nobody
received it; with nothing issued, Verify says so. A table with copies on the
log that is not loaded (an upload, after a restart) is named, so it can be
loaded again; the example table is the same every time it is loaded. Every
carrier is read back, each by its own stage:
exact fingerprint, canary rows and allocation by exact matching, and low-order
bits, noise, free choices, tuple ordering, redaction and the dummy column by
fuzzy matching against the source.

## Column roles

Roles are suggested from the data and shown as a selector in each column
header of the preview, where the data owner confirms or changes them:

- **Identifier:** a text or whole-number column whose values are distinct. Marks
  are keyed to it, and it matches a recovered row back to the source. With no
  identifier, rows are recognised by the columns marking leaves alone.
- **Tolerant:** suggested for a decimal column with at least three decimals, or
  a timestamp with fractional seconds, and available for any column with a
  decimal place, together with how much its values may change. Only these
  carry low-order-bit and noise marks, so nothing without a stated tolerance is
  ever altered.
- **Redact:** suggested for text columns whose header names a person or their
  contact details (name, first name, surname, email, phone, address, and the
  German and French equivalents) or whose values are email addresses. Dates
  and numbers are never suggested. Only these are replaced by pseudonyms.
- **Untouched:** everything else.

CSV files are read in their own dialect: the delimiter (comma, semicolon, tab
or pipe) is detected, a byte order mark is stripped, and decimal commas as
Swiss and German Excel writes them (`7951,14`, `1.204,00`) are read as
numbers. The Mark tab shows what was detected and takes an override. Marked
copies are written back in the source's dialect.

## Techniques

| Technique | How it works |
|---|---|
| Canary rows | About 0.5% synthetic rows per recipient, built from a real row. The key is drawn from inside the range and format the key column already uses, avoiding every key in the source; personal columns are recombined from other rows. Seeded per dataset and per recipient, so two tables never share a canary. |
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
- Merged copies are detected and every recipient involved is named, but the
  demo does not work out which rows came from whom beyond the canaries.
- A key column with no gaps (1000 to 1499, every value used) leaves canaries
  no room inside its range, so they take keys past the top and show in a copy
  sorted by key. The Mark tab says so when it happens.
- The detector reads every copy of a table under one set of column roles, so
  the roles are fixed once a copy is issued (the server refuses a change, and
  the Mark tab says why). Recalling the copies unlocks them. The roles are
  stored with the log, so a restart does not change them.
- Allocation names a copy only when none of the rows withheld from it are
  present and a file covering this much of the source would miss them all by
  chance at most once in 1,000 times. A small sample of a small table
  therefore rarely attributes by allocation alone.
- Covert marking is a technical property. Whether recipients are told is a legal
  and policy decision.
