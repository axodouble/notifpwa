# ADR-006: The UI wears the jas.pe palette and its editorial idiom

**Date:** 2026-10-01
**Author:** jasper
**Status:** Accepted

## Context

Every project of the operator's inherits one visual language, documented at
[jas.pe/css/style.css](https://jas.pe/css/style.css) and in the *Colors & branding* post
(<https://jas.pe/posts/colors/>). This app already used most of it — the same Neutrals
(Dim Grey, Charcoal Blue, Coarse Wool, White Smoke, the Teal-Grey hairline) and the same
three faces (Rethink Sans, DM Mono, Lora) — but with a Lobster-red accent and a card
idiom: rounded 14px boxes, filled surfaces, boxed status lines. So it read as a generic
PWA wearing the brand's jewellery rather than as part of the same family.

Restyling raised three questions the swatch list cannot answer on its own.

**Contrast.** The blog sets `--accent` to Greenish `#0a685a` in *both* schemes, so its
own dark mode renders headings at 2.46:1 against Coarse Wool and button-hover states at
1.84:1. Acceptable for a blog's large headings; not for a 15px admin table this app's
operator actually reads to delete a token. The palette already contains the fix: Muted
Teal `#8aa29e` is the same hue family at 6.07:1 on Coarse Wool, and the app's outgoing
`--ok` used exactly that trick (`light-dark(#4f736d, #8aa29e)`).

**Errors.** Once the accent is green, green can no longer mean *something is wrong* —
and the previous design leaned on that: `--err` was the same red as `--accent`, so a
failing subscription and a call to action were the same colour. Nothing in the swatch
set expresses failure.

**Where the palette should live.** Both pages carried their own inline copy of the token
block, so any restyle would have had to be done twice and kept in step twice.

## Decision

**Adopt the blog's stylesheet, copied rather than linked.** `web/style.css` holds the
swatch set, the core roles, and the per-element hooks under the same names and values as
jas.pe, so the two files stay diffable, and is served at `/style.css` and linked from
both pages. No cross-origin dependency: an installed PWA on a phone must render the same
way with the blog unreachable.

**The idiom moves with the colours.** Framed column with a hairline down each edge,
header and footer bands, green headings in Rethink Sans, prose in Lora, labels and
metadata in uppercase DM Mono at the blog's tracking. Rounded cards become hairline-ruled
rows and bands; form fields become underlined rather than boxed; the primary action
becomes the blog's uppercase pill. No dropcap — that rule belongs to article prose.

**Dark scheme promotes the accent, light scheme is byte-loyal.** `--accent`, `--muted`
and `--ok` step up to Muted Teal / Teal Deep in the dark scheme. A *filled* accent keeps
the green in both, because green with a Smoke label is 5.76:1 wherever it lands while the
dark-scheme accent is far too light to sit text on — hence `--accent` (the accent you
read) being separate from `--accent-solid` (the accent you press).

**Errors keep the red, and never carry meaning by colour alone.** `--color-lobster-deep`
stays as the only value outside the swatch set, and a status line puts its kind on the
hairline beside the text rather than in the text itself: Lobster on Coarse Wool is 3.24:1,
and an unreadable failure is a worse outcome in a notification app than an off-brand hue.

**Follow the system, no toggle.** The blog ships a three-state theme button because
reading long-form at 2am is its own problem. This is a one-action utility, and the server
already answers `Sec-CH-Prefers-Color-Scheme` so the manifest's `background_color` and
`theme_color` match the scheme before first paint — state a JS toggle would have to
re-implement.

## Consequences

- The palette exists once. A third page costs one `<link>`, and a restyle cannot leave a
  page behind — which the test suite now enforces on both pages.
- Drift against the blog is now possible, and accepted: the file is a copy, so a change
  upstream lands here only when someone ports it. Grep-able token names are the mitigation.
- We deliberately do not copy the blog's inline-code treatment. There it is
  `--code-bg: light-dark(var(--fg), var(--accent))` with `code { color: var(--smoke) }`,
  and `--smoke` is not a declared token, so light-mode inline code renders charcoal on
  charcoal. Anyone diffing the two stylesheets should not "fix" ours to match.
- Light mode is identical to the blog; dark mode is measurably more legible and one notch
  lighter in hue. Someone comparing the two sites in dark mode will see Muted Teal labels
  where the blog has deep green.
- The green/red split gives us two semantic colours for two semantic states. A third
  state — a warning, say — has no honest expression in the set yet.
