// The app's icons: hand-written paths, and the one function that draws them.
//
// No icon library. The CSP and the bundled-renderer allowlist would both have
// to be widened for one, and it would arrive with four hundred glyphs to get
// the six this window draws. The same argument the stylesheet makes about the
// title bar's icons, kept in one place now that a second surface needs them.
//
// Every path is on a 16-unit grid and drawn with stroke rather than fill, so
// one rule in the stylesheet — width, weight, currentColor — gives an icon
// the colour of whatever button it lands in. An icon that carried its own
// palette would be the one thing on screen that ignored the theme.
//
// They are built with createElementNS, never from a markup string. The
// renderer holds the line that markup is not parsed from strings anywhere,
// and a decorative glyph is not a good enough reason to open that door for
// the first time — SVG is also the shape most likely to be pasted in from
// somewhere else, which is exactly when the door matters.

const SVG_NS = "http://www.w3.org/2000/svg";

export function buildIcon(paths) {
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  // The glyph says nothing a screen reader needs: the button around it
  // carries the words, and reading both would say the action twice.
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");
  for (const d of paths) {
    const path = document.createElementNS(SVG_NS, "path");
    path.setAttribute("d", d);
    svg.appendChild(path);
  }
  return svg;
}

// A thumbtack, seen head-on and pointing down.
export const PIN_ICON = [
  "M6 1.9h4",
  "M6.6 1.9v3.5a2.6 2.6 0 0 1-1.4 2.3l-.8.5a1.2 1.2 0 0 0-.6 1v.3h8.4v-.3a1.2 1.2 0 0 0-.6-1l-.8-.5a2.6 2.6 0 0 1-1.4-2.3V1.9",
  "M8 9.5v4.6",
];

// Three dots. The zero-length segments are deliberate: a round line cap turns
// each into a dot, which keeps the whole set on one stroke rule instead of
// making this the only icon that fills.
export const MORE_ICON = ["M3.6 8h.01", "M8 8h.01", "M12.4 8h.01"];

// Two sheets, the front one offset — the shape every clipboard action on this
// platform uses, so it needs no learning.
export const COPY_ICON = [
  "M6.5 5.5h6a1 1 0 0 1 1 1v6a1 1 0 0 1-1 1h-6a1 1 0 0 1-1-1v-6a1 1 0 0 1 1-1z",
  "M3.5 10.5a1 1 0 0 1-1-1v-6a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1",
];

// The answer to a copy: shown in the button for a moment, in place of the
// sheets. A tick is the one glyph that needs no tooltip to be understood.
export const CHECK_ICON = ["M3.2 8.4 6.4 11.6 12.8 4.8"];

// A circular arrow: run it again.
export const REGENERATE_ICON = [
  "M13.6 8a5.6 5.6 0 1 1-1.7-4",
  "M13.9 2.3v3.1h-3.1",
];

// A pencil: the words come back, editable, rather than being re-sent as they
// were.
export const EDIT_ICON = [
  "M11.4 2.8a1.7 1.7 0 0 1 2.4 2.4l-7.5 7.5-3.2.8.8-3.2z",
  "M10.3 3.9l2.4 2.4",
];
