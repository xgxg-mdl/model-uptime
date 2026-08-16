# Admin Responsive Layout Research

## Decision

Do not keep converting the eight-column service table into vertically labelled
cells on small screens. Keep the semantic table on desktop and render a separate
summary list below `960px`. Each service summary should use three compact rows:

```text
[ ] Service name  #service-id                         status
    protocol · model · provider                       interval
                              [edit] [copy] [test] [delete]
```

The normal row must not reserve space for a test result. A result appears only
after a test is started.

## Primary-Source Findings

### Grafana Saga

Grafana states that `InteractiveTable` is not responsive and recommends a
summary list plus a single-item view when mobile support is required. It also
recommends moving nonessential fields out of the primary view instead of making
the main table visually noisy.

- [InteractiveTable guidance](https://grafana.com/developers/saga/components/interactive-table/)
- [Lists of Objects](https://grafana.com/developers/saga/templates/lists-of-objects/)
- [Table Page layout](https://grafana.com/developers/saga/templates/table/)

Grafana's icon-button guidance uses compact controls for toolbars and tables,
with a tooltip and accessible name for every icon.

- [IconButton](https://grafana.com/developers/saga/components/buttons/iconButton/)

Application: retain a real table for desktop comparison, use an independent
mobile list, and expose the four service commands as one icon-button group.

### Uptime Kuma

Uptime Kuma is the closest operational reference. Its production monitor rows
use roughly `12-15px` padding and keep identity and status compact. Its deployed
CSS uses separate breakpoints for monitor filtering, mobile navigation, and
generic tables instead of collapsing the entire application at one width.

- [Official status instance](https://status.kuma.pet/)
- [Deployed CSS](https://status.kuma.pet/assets/index-s7FngxXa.css)
- [Deployed JavaScript](https://status.kuma.pet/assets/index-vTT89PEn.js)

Application: use content-specific breakpoints, keep row padding small, and keep
actions in one horizontal group.

### Gatus

The official Gatus instance is mobile-first: endpoint records are one column by
default, two columns from `640px`, and three from `1024px`. A record uses compact
title, status, metadata, and heartbeat regions; it does not expand every
attribute into a labelled form row.

- [Official instance](https://status.twin.sh/)
- [Deployed CSS](https://status.twin.sh/css/app.css)
- [Deployed JavaScript](https://status.twin.sh/js/app.js)

Application: "one column" describes the list of records, not the internal
layout of each record. Keep identity and status on the first line and compress
secondary fields into one metadata line.

### Beszel

Beszel's official dashboard image shows many systems and their principal
metrics at once. It achieves density through aligned columns, short labels, and
limited semantic color rather than many nested surfaces.

- [Official product page](https://beszel.dev/)
- [Dashboard image](https://beszel.dev/image/home-dashboard.png)

Application: preserve the desktop table and its stable column positions.

### W3C

WCAG 2.2 requires pointer targets to be at least `24x24 CSS px`, unless a listed
spacing exception applies. The implementation uses `44x44px` row actions on
narrow screens. This exceeds the minimum with a comfortable touch margin and
still fits four commands on one line at the supported 320px viewport.

- [Target Size (Minimum)](https://www.w3.org/WAI/WCAG22/Understanding/target-size-minimum.html)

At `320 CSS px`, reflow should not lose information or functionality or require
two-dimensional page scrolling. Real data tables may scroll internally, but a
mobile summary list is more suitable for this named-object workflow.

- [Reflow](https://www.w3.org/WAI/WCAG22/Understanding/reflow.html)
- [Table Tips](https://www.w3.org/WAI/tutorials/tables/tips/)
- [Tables with one header](https://www.w3.org/WAI/tutorials/tables/one-header/)

Required small text must meet the WCAG AA contrast ratio of at least `4.5:1`.
The shared `--fg-dim` and `--fg-mute` aliases are audited against the terminal,
panel, and input backgrounds; essential hints and headers use `--dim`, while
`--mute` remains reserved for disabled or decorative content.

- [Contrast (Minimum)](https://www.w3.org/WAI/WCAG22/Understanding/contrast-minimum.html)

## Implementation Requirements

- `>= 960px`: render the semantic service table with scoped column headers.
- `< 960px` (`max-width: 959px`): render a dedicated service summary list with no horizontal table
  scroll and no simulated `data-label` cells.
- `< 560px` (`max-width: 559px`): collapse long forms to one column; keep wider forms two-column at
  `640px` and `768px`.
- Mobile service summaries show checkbox, name, ID, state, protocol, optional
  model/provider, interval, and all four row commands within about `110px`.
- Mobile commands use one row of stable `44x44px` icon buttons with tooltips and
  accessible names. The larger target reduces accidental activation of the
  destructive command while preserving the compact three-row summary.
- Current and latest versions remain side by side on mobile; status spans the
  next row.
- The normal toolbar shows selection state and the new-service action on one
  row. Batch commands appear only when at least one service is selected.
- Desktop and mobile selections stay synchronized across responsive changes.
- Preserve the neutral palette roles and semantic status colors. The shared
  dim/mute aliases are intentionally brighter than the original values so
  required small text remains readable on both page backgrounds. Service rows
  use the panel background with separators, not the input background as a
  nested card surface.
- Use `--dim` for information that must be read. Reserve `--mute` for disabled
  or decorative content and use accent colors only for primary actions, focus,
  status, and meaningful syntax tokens.

## Acceptance Checks

- At `320px` and `390px`, the page has no horizontal overflow, all four service
  commands remain on one line, and an untested service is no taller than about
  `110px`.
- At `640px` and `768px`, the summary list remains active while ordinary forms
  retain two columns.
- At `960px` and above, the semantic table is visible and its columns align.
- Every icon action has a `title`, an accessible name, and at least a `44x44px`
  target in the summary list.
- The service list adds no third surface color, card border, or card radius.
- Responsive regression tests validate the final list/table behavior rather
  than the removed pseudo-label card implementation.

## Evidence Limitation

The repository required GitHub access through `gh`. The local CLI was not
authenticated, so no GitHub source-code claims were used and no alternative
GitHub access path was attempted. All conclusions above rely on official design
documentation, official product pages, official production assets, and W3C
standards. All cited URLs returned HTTP 200 during this research.
