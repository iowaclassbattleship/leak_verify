# Custodial

Two clickable prototypes that run the whole **mark → leak → detect** loop on one host.
They use a Go backend (standard library plus `golang.org/x/image` for font rasterizing) and a plain HTML/JS frontend with no build step.
All state is kept in memory, and nothing leaves the machine.

```sh
go run .                          # http://127.0.0.1:8080
go run . -addr 127.0.0.1:9000     # other port
go test ./...                     # robustness tests for both modules
```

The marking key is random per process, so restarting the server clears all state.

The frontend is served straight from `web/`, not bundled into the binary: edit an HTML, CSS or JS file and just refresh the browser. Go changes still need a restart. Run from the project directory, or point `-web` at the directory:

```sh
go run . -web /path/to/custodial/web
```

The two modules are separate frontends served by the same local backend:

| URL | App | Code |
|---|---|---|
| `/` | Landing page linking both modules | `web/index.html` |
| `/dataset/` | Dataset attribution | `web/dataset/` |
| `/document/` | Document attribution, with **Tag** and **Verify** tabs | `web/document/` |

`web/shared/` holds the stylesheet and the DOM/API helpers both apps import.

## Module A: dataset attribution (`internal/tabular`)

| Technique | How it works |
|---|---|
| Canary rows | About 0.5% synthetic accounts per recipient, from the same generator as real rows. They are matched by ID, email, or name+city+balance. |
| Low-order-bit mark | For each tolerant cell, a keyed HMAC of the primary key decides whether the cell carries a bit, which codeword bit it carries, and a mask. The value's last-digit parity holds the bit. Only fields with a declared tolerance are touched: `opened_at` ms, lat/lon at 1e-6, `risk_score` at 1e-4. |
| Dummy column | `branch_ref` = `BR-<id XOR keyed pad>-<check>`. Easy to drop, and included only as the weakest layer. |

Detection runs in two stages:
- **Exact matching:** SHA-256 row fingerprints compared against each recipient's regenerated copy.
- **Fuzzy matching:** majority vote per codeword bit, then ECC decoding.

If `account_id` is dropped, rows are re-identified against the source by email or name+city.

## Module B: document attribution (`internal/document`)

The uploaded PDF's text is extracted and re-typeset into a PDF with an embedded font. The demo writes and parses its own PDFs.

| Layer | Embedding | Survives | Breaks |
|---|---|---|---|
| Layout | Each body line's baseline shifted ±0.45 pt (line-shift coding) | re-save, screenshot, print+scan | copy-paste |
| Frequency domain | Spread-spectrum watermark: a keyed 128 px tile of 384 mid-band sinusoids (96 sync pilots, 9 per codeword bit), tiled every 64 pt behind the page with Multiply blending, 0.9 grey levels of texture on a 253 base | re-save, screenshot, cropped/rescaled/JPEG screenshot | printing, removing background images |
| Object layer | 1.5% grey tint on a 14 pt cell grid behind the text | re-save, lossless screenshot | printing (below toner threshold) |
| Metadata | `LAPRef` tag with a keyed check in the Info dictionary | byte-identical forward | any re-save |

No layer marks the words themselves, so nothing survives copy-pasting the text.

How the frequency-domain detector works:
- **PDF:** take the tile image from the page resources and correlate it with the keyed basis functions.
- **Image:** mask ink and high-pass the background, then find the tile period: from the page width for a full page, otherwise by sweeping the plausible range and scoring how much energy lands in the watermark band. Unused frequencies of the same band are the noise reference, so the score does not favour one capture resolution over another. The best periods are refined against the pilots, then the capture is folded into one tile, the offset is found and each bit's sign is read. A pilot correlation of 6 sigma is required before any bits are read.

The band sits at roughly 1.5 to 3 mm features on the page. Finer would be less visible but does not survive a rescaled JPEG; coarser is easy to see on a blank page.

The attack simulations (used by `go test`, not shown in the UI) run on the real files:
- **Re-save:** parse and rewrite the PDF.
- **Re-save without backgrounds:** rewrite the PDF with its image objects removed.
- **Copy-paste:** text extraction.
- **Screenshot:** rasterize page 1 at 96 dpi to PNG.
- **Cropped screenshot via chat:** page 1 at 110 dpi, cropped to about a quarter of the page, scaled to 75%, JPEG q70.
- **Print+scan:** 150 dpi with highlight clipping, blur, 1.4% scale and offset, noise, and JPEG.

The app has two tabs:
- **Tag:** a three-step share flow.
  1. **Document:** drop a PDF or text file, or use the sample.
  2. **Recipients:** pick recipients from the roster or add new ones. Protection layers are under a collapsible panel.
  3. **Share:** download each recipient's tagged PDF, named `<document>_<recipient>.pdf`, or all of them as one zip. Every copy is recorded in the issuance log.
- **Verify:** drop or choose one or more files (PDF, PNG or JPEG). Each gets a result card: *Tagged* (with recipient and mark ID), *Possible tag*, or *No tag found*, plus per-layer evidence.

Verification reads every layer against the current document's issuance log. A file is *Tagged* when a unique recipient matches with chance-match probability ≤ 1e-3. It counts as *No tag found* when the best evidence is at chance level (≥ 5%).

The issuance log applies a privilege hierarchy: a viewer only sees recipients in their own subtree, and only the Security Office sees mark IDs. It is kept by the backend (`GET /api/doc/state?viewer=…`) but no longer has a view in the UI.

## Shared codec (`internal/codec`)

- **Codeword:** each 16-bit mark ID becomes 4 nibbles, each encoded with extended Hamming(8,4). The resulting 32 bits are XORed with a keyed whitening mask.
- **ID allocation:** new IDs keep a codeword distance of at least 12 from every existing ID.
- **Decoding:** soft decision per nibble. Candidates from the issuance log are ranked by bit agreement.
- **Attribution rule:** the best candidate must be unique, and the chance that an unrelated artifact matches this well must be ≤ 1e-3 (binomial tail × number of candidates).

## Honest limits

- Uploaded PDFs are re-typeset. Production would mark the original content stream in place.
- Print+scan is simulated. Rotation and photos of screens are not modelled. The layout and tint-grid decoders assume a full upright page; the frequency-domain decoder handles crops and rescaling but not rotation.
- The frequency-domain watermark is tuned to be invisible on screen, so it does not survive printing. It also lives in a separate background image that editors can delete.
- No layer marks the words themselves, so copy-pasted text carries no tag.
- Collusion is only flagged when techniques disagree; it is not traced.
- Covert marking is a technical property. Whether recipients are told is a legal and policy decision.
