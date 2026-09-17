# Custodial

Document marking and attribution. Every recipient gets an identical-looking copy
of a Word, PowerPoint, Excel or PDF file, carrying an invisible recipient-unique
mark, so a leaked copy can be traced back to the person it was issued to.

Runs on one host as a local web app: a Go backend (standard library plus
`golang.org/x/image` for font rasterizing) and a plain HTML/JS frontend with no
build step. All state is in memory, and nothing leaves the machine.

The dataset counterpart lives in its own repository, `../provenance`.

```sh
go run .                          # http://127.0.0.1:8080
go run . -addr 127.0.0.1:9000     # other port
go test ./...                     # robustness tests
```

The marking key is random per process, so restarting the server clears all state.

The frontend is served straight from `web/`, not bundled into the binary: edit an HTML, CSS or JS file and just refresh the browser. Go changes still need a restart. Run from the project directory, or point `-web` at the directory:

```sh
go run . -web /path/to/custodial/web
```

## Open XML files: Word, PowerPoint, Excel (`internal/office`)

An Open XML file is a zip of XML parts, so marking edits those parts and rewrites
the archive. The readable text is never changed.

| Carrier | Embedding | Survives | Breaks |
|---|---|---|---|
| Background watermark | The same spread-spectrum tile the PDF pipeline uses, 64 pt square, tiled behind the page (Word header shape), the slide master (PowerPoint `blipFill`) or the sheet (Excel sheet background) | editing the text, re-saves, export to PDF, a screenshot of the rendered page | deleting the image from the package, copying the content into a new file |
| Custom XML part | The identifier in `customXml/item1.xml`, referenced from the main part | editing the text, re-saves | the document inspector, copying the content into a new file |
| Spacing | Character spacing of 1/20 pt per word (Word), 1/100 pt per word (PowerPoint), or the last digit of row heights and column widths (Excel) | re-saves that keep formatting, the document inspector | retyping the runs, copying the text out, converting the file |
| Invisible characters | A zero-width character after about one word in eight, on a keyed subset of words | copy and paste into another document | any tool that strips invisible characters |
| Metadata | A custom document property | a plain forward | the document inspector |

The first two carriers never touch the text or its formatting, so a recipient can
keep working on the file without disturbing them. That is what makes them the
default pair: the background watermark carries real bit evidence and reads back
from a rendering, and the custom XML part carries the identifier through any
amount of editing.

Each slot is keyed to a word and the word before it, not to its position, so the
mark still reads back after text is reordered or partly deleted, and a small
vocabulary still reaches many of the 32 codeword bits. The app reports how many
bits each carrier can reach in the loaded file and warns when that is under 32.

The invisible-character layer is the only one that changes the text bytes, so it
is **off by default**: switch it on when surviving copy and paste matters more
than leaving the text untouched. Spacing and metadata never alter the text.

Attacks simulated in the tests: forward, re-save, edit the document (runs
retyped, so their formatting is replaced), run the document inspector, strip
invisible characters, remove the watermark image, and copy-paste the text.

`TestBackgroundSurvivesRendering` runs the rendering path for real where
LibreOffice and poppler are installed: it marks each fixture, exports it to PDF
and captures the first page at 96 dpi. All three formats recover all 32 bits
from both the export and the capture, with no bit errors. The Verify tab
therefore accepts a PDF export or a screenshot of an Open XML file, not only the
file itself.

Excel is the weak case. Its spacing carrier reaches only about 9 of 32 bits on a
small sheet, which the app reports, and a sheet background shows on screen but
does not print from Excel.

## PDFs (`internal/document`)

A PDF is reduced to its text and re-typeset with an embedded font, so the demo writes and parses its own PDFs. Office files, by contrast, are marked in place.

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
- **Verify:** drop or choose one or more files (the Open XML file, a PDF export or screenshot of it, or PDF, PNG or JPEG for the PDF pipeline). Each gets a result card: *Tagged* (with recipient and mark ID), *Possible tag*, or *No tag found*, plus per-layer evidence.

Verification reads every layer against the current document's issuance log. A file is *Tagged* when a unique recipient matches with chance-match probability ≤ 1e-3. It counts as *No tag found* when the best evidence is at chance level (≥ 5%).

The issuance log applies a privilege hierarchy: a viewer only sees recipients in their own subtree, and only the Security Office sees mark IDs. It is kept by the backend (`GET /api/doc/state?viewer=…`) but no longer has a view in the UI.

## Codec (`internal/codec`)

- **Codeword:** each 16-bit mark ID becomes 4 nibbles, each encoded with extended Hamming(8,4). The resulting 32 bits are XORed with a keyed whitening mask.
- **ID allocation:** new IDs keep a codeword distance of at least 12 from every existing ID.
- **Decoding:** soft decision per nibble. Candidates from the issuance log are ranked by bit agreement.
- **Attribution rule:** the best candidate must be unique, and the chance that an unrelated artifact matches this well must be ≤ 1e-3 (binomial tail × number of candidates).

## Honest limits

- Uploaded PDFs are re-typeset. Production would mark the original content stream in place.
- Print+scan is simulated. Rotation and photos of screens are not modelled. The layout and tint-grid decoders assume a full upright page; the frequency-domain decoder handles crops and rescaling but not rotation.
- The frequency-domain watermark is tuned to be invisible on screen, so it does not survive printing. It also lives in a separate background image that editors can delete.
- The background watermark in an Open XML file was verified against LibreOffice's renderer. Word, PowerPoint and Excel place tiled backgrounds slightly differently, so a production build would verify each one.
- No layer marks the words themselves, so copy-pasted text carries no tag.
- Collusion is only flagged when techniques disagree; it is not traced.
- Covert marking is a technical property. Whether recipients are told is a legal and policy decision.
