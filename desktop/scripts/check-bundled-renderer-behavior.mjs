#!/usr/bin/env node
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import vm from "node:vm";

// The renderer ships as ES modules, so this file loads it the way the webview
// does: a real module graph, linked and evaluated. vm.SourceTextModule is the
// only API that can do that inside a synthetic global, and it is behind a
// flag — so re-exec ourselves with it rather than making every caller
// (check-bundled-renderer.sh, the Makefile, a developer) remember.
//
// The alternative was to concatenate the modules and run them as one script.
// That would have passed while proving less: a missing import is a link-time
// error here and an invisible no-op there, and "the files work together" is
// exactly the property the split put at risk.
if (typeof vm.SourceTextModule !== "function") {
  const result = spawnSync(
    process.execPath,
    ["--experimental-vm-modules", "--no-warnings", new URL(import.meta.url).pathname, ...process.argv.slice(2)],
    { stdio: "inherit" },
  );
  process.exit(result.status === null ? 1 : result.status);
}

const repoRoot = path.resolve(path.dirname(new URL(import.meta.url).pathname), "..", "..");
const rendererDir = process.env.WORKMAX_BUNDLED_RENDERER_DIR
  ? path.resolve(process.env.WORKMAX_BUNDLED_RENDERER_DIR)
  : path.join(repoRoot, "desktop/renderer/en/desktop");
const rendererPath = path.join(rendererDir, "renderer.js");
// The entry module and every module it pulls in. The discipline checks below
// (no innerHTML, no console, no web storage) run over the concatenation
// of all of them, because "the renderer does not do X" has to keep meaning the
// whole renderer after the split, not just the file that used to be all of it.
const RENDERER_MODULES = [
  "renderer.js",
  "dom.js",
  "protocol.js",
  "events.js",
  "markdown.js",
  "transcript.js",
  "composer.js",
  "threads.js",
  "context-panel.js",
  "mind.js",
  "fence.js",
];
const moduleSources = new Map(
  RENDERER_MODULES.map((name) => [name, fs.readFileSync(path.join(rendererDir, name), "utf8")]),
);
const rendererSource = [...moduleSources.values()].join("\n");
const rendererHTML = fs.readFileSync(path.join(rendererDir, "index.html"), "utf8");
const rendererCSS = fs.readFileSync(path.join(rendererDir, "styles.css"), "utf8");

// Every module the renderer ships must be on the list above, and every module
// on the list must exist. Otherwise a new file could join the graph — and be
// served to the webview — while the discipline checks quietly stopped covering
// the code that moved into it.
{
  const onDisk = fs
    .readdirSync(rendererDir, { withFileTypes: true })
    .filter((entry) => entry.isFile() && entry.name.endsWith(".js") && entry.name !== "shim.js")
    .map((entry) => entry.name)
    .sort();
  assert.deepEqual(
    onDisk,
    [...RENDERER_MODULES].sort(),
    "the renderer module list and the files on disk disagree",
  );
}

// --- Appearance cascade ------------------------------------------------------
//
// Three states (follow the system / force light / force dark) out of two dark
// token blocks. The blocks are literal duplicates by design — CSS has no way to
// apply one declaration list from both a media query and a plain selector — so
// the risk the design accepts is that they drift. This is where that risk is
// paid off: the two must declare the same properties with the same values, the
// media-query copy must be guarded so a forced light theme survives a dark
// system, and the forced-dark copy must come last so it survives a light one.
{
  const darkInMedia = rendererCSS.match(
    /@media\s*\(prefers-color-scheme:\s*dark\)\s*\{\s*:root:not\(\[data-theme="light"\]\)\s*\{([\s\S]*?)\n\s*\}/u,
  );
  assert.ok(
    darkInMedia,
    'the system-dark block must be guarded by :root:not([data-theme="light"]) — without it, ' +
      '"system dark + user chose light" leaves the dark tokens winning and the choice does nothing',
  );
  const forcedDarkAt = rendererCSS.indexOf(':root[data-theme="dark"] {');
  assert.ok(forcedDarkAt > 0, "a :root[data-theme=\"dark\"] block must exist");
  assert.ok(
    forcedDarkAt > rendererCSS.indexOf("@media (prefers-color-scheme: dark)"),
    "forced dark must come AFTER the media query: equal specificity, so source order decides",
  );
  const forcedDark = rendererCSS
    .slice(forcedDarkAt)
    .match(/:root\[data-theme="dark"\]\s*\{([\s\S]*?)\n\}/u);
  const declarations = (body) =>
    body
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line.startsWith("--"));
  assert.deepEqual(
    declarations(forcedDark[1]),
    declarations(darkInMedia[1]),
    "the two dark token blocks have drifted; they must declare identical values",
  );
  assert.ok(declarations(forcedDark[1]).length > 20, "the dark block must carry the whole palette");
  // The tokens the highlighter paints with have to exist in both themes, or a
  // code block loses its colours in one of them.
  for (const token of ["comment", "keyword", "string", "number", "function", "builtin", "punct", "tag", "attr"]) {
    assert.match(rendererCSS, new RegExp(`\\.tok-${token}\\s*\\{`, "u"));
    assert.ok(
      declarations(forcedDark[1]).some((line) => line.startsWith(`--code-${token}:`)),
      `--code-${token} must be defined for the dark theme`,
    );
  }
}

// --- Folding the columns -----------------------------------------------------
//
// This stub does not evaluate CSS, so the rules that collapse a column cannot
// be observed the way a hidden attribute can. They are pinned as text instead,
// because the alternative — nothing — is how "the panel shows Run overview 0/4
// over four Waiting rows on a screen with no conversation" came back once
// already. The automatic rule is derived from #thread-panel[hidden], which five
// call sites in three modules already maintain; a class toggled next to each of
// them is the drift this avoids.
{
  assert.match(
    rendererCSS,
    /\.shell:has\(#thread-panel\[hidden\]\)\s*\.context-panel\s*\{\s*display:\s*none;/u,
    "with no conversation selected the context column must fold away, not draw an empty scaffold",
  );
  // And the track it leaves behind goes to zero — but ONLY when the mind is
  // not the panel in it. This used to be a two-column grid-template-columns,
  // which was correct while the column had one occupant and became a bug the
  // moment it had two: a template that drops track 3 folds the mind away with
  // the workspace, and a mind is not a property of the conversation. The width
  // variable can be guarded; the template could not.
  assert.match(
    rendererCSS,
    /:root:not\(\[data-right-panel="mind"\]\)\s*\.shell:has\(#thread-panel\[hidden\]\)\s*\{\s*--rail-right:\s*0px;/u,
    "with no conversation the workspace half of the column must collapse — and only that half",
  );
  assert.doesNotMatch(
    rendererCSS,
    /\.shell:has\(#thread-panel\[hidden\]\)\s*\{\s*grid-template-columns/u,
    "a template swap cannot express 'fold this column unless the mind is in it'",
  );
  // The narrow-window fold sets the rail width through --rail rather than a
  // second grid-template-columns: the :has() rule above outranks a bare
  // .shell, so a competing declaration there would silently lose and leave a
  // 300px rail on a 1000px window.
  assert.doesNotMatch(
    rendererCSS,
    /@media \(max-width: 980px\) \{\s*\.shell \{\s*grid-template-columns/u,
    "the narrow-window rails must be set via --rail, not a lower-specificity grid declaration",
  );

  // The MANUAL folds obey the same constraint, and it is sharper for them:
  // they are keyed off an attribute on <html>, which is (0,2,0) plus a
  // descendant — always less specific than :has(#thread-panel[hidden]), which
  // carries an id. So each one takes its column to zero through the custom
  // property nothing else declares, never by restating the template.
  assert.match(
    rendererCSS,
    /\.shell\s*\{[^}]*--rail-left:\s*var\(--rail\);[^}]*--rail-right:\s*var\(--rail\);[^}]*grid-template-columns:\s*var\(--rail-left\) minmax\(0, 1fr\) var\(--rail-right\);/u,
    "each rail must be its own grid track variable, or one cannot fold without the other",
  );
  for (const [selector, property] of [
    [':root\\[data-sidebar="collapsed"\\] \\.shell', "--rail-left"],
    [':root\\[data-right-panel="none"\\] \\.shell', "--rail-right"],
  ]) {
    assert.match(
      rendererCSS,
      new RegExp(`${selector}\\s*\\{\\s*${property}:\\s*0px;\\s*\\}`, "u"),
      `${property} is how the manual fold wins; a grid-template-columns here would lose to the :has() rule`,
    );
  }
  assert.match(
    rendererCSS,
    /:root\[data-sidebar="collapsed"\]\s*\.sidebar\s*\{\s*display:\s*none;/u,
    "a folded rail must be gone, not a zero-width column still painting its border",
  );
  assert.match(
    rendererCSS,
    /:root\[data-right-panel="none"\]\s*\.right-rail\s*\{\s*display:\s*none;/u,
    "same for the right column, whichever panel was in it",
  );
  // One column, one panel. Two rules, and they have to be a PAIR: either alone
  // leaves a state where both panels are in track 3 at once, stacked on top of
  // each other in a 300px column.
  assert.match(
    rendererCSS,
    /:root\[data-right-panel="mind"\]\s*\.context-panel\s*\{\s*display:\s*none;/u,
    "showing the mind must put the workspace panel away",
  );
  assert.match(
    rendererCSS,
    /:root:not\(\[data-right-panel="mind"\]\)\s*\.mind-panel\s*\{\s*display:\s*none;/u,
    "and the mind is in the column only while it is the one asked for",
  );
  // Below 1180px the workspace column is not a choice — the window cannot
  // afford it — so the button that would pretend otherwise goes with it. The
  // mind survives the same query: nobody asks for the workspace panel, it is
  // simply there, but the mind is on screen only because somebody pressed the
  // brain, and "not at this size" is not an answer to that — on a 900px window
  // it would mean no way to create, switch or teach a mind at all.
  const narrow = rendererCSS.match(/@media \(max-width: 1180px\) \{([\s\S]*?)\n\}/u);
  assert.ok(narrow, "the narrow-window fold must still exist");
  assert.match(
    narrow[1],
    /#context-panel-button\s*\{\s*display:\s*none;/u,
    "a toggle that cannot change what is on screen is worse than no toggle",
  );
  assert.match(
    narrow[1],
    /:root:not\(\[data-right-panel="mind"\]\)\s*\.shell\s*\{\s*--rail-right:\s*0px;/u,
    "the narrow-window fold takes the workspace column, not an explicitly opened mind",
  );
  assert.doesNotMatch(
    narrow[1],
    /\n\s*\.mind-panel\s*\{\s*display:\s*none;/u,
    "the mind must stay reachable at every width this window can be resized to",
  );
}

// --- The window's title bar carries the window-level controls -----------------
//
// The native title bar is hidden (HiddenInset, desktop/wails/main.go), so the
// webview paints the strip the traffic lights sit on. The three view controls
// — fold the rail, search, fold the workspace column — live there next to the
// lights, not inside any column: a control that hides its column cannot bring
// it back, and one toggle per column is the whole grammar.
{
  const titlebarAt = rendererHTML.indexOf('id="titlebar"');
  const shellAt = rendererHTML.indexOf('<main class="shell">');
  assert.ok(titlebarAt > 0 && shellAt > titlebarAt, "the title bar precedes the shell");
  const titlebarHTML = rendererHTML.slice(titlebarAt, shellAt);
  for (const id of ["sidebar-collapse-button", "sidebar-search-button", "context-panel-button"]) {
    assert.match(
      titlebarHTML,
      new RegExp(`id=["']${id}["']`, "u"),
      `${id} is a window-level control and belongs in the title bar`,
    );
  }
  // The brand row is only the brand again: the icons that shared it moved up.
  const brandAt = rendererHTML.indexOf('class="brand"');
  const toolbarAt = rendererHTML.indexOf('class="thread-toolbar"');
  assert.ok(brandAt > 0 && toolbarAt > brandAt, "the brand row still heads the rail");
  assert.doesNotMatch(
    rendererHTML.slice(brandAt, toolbarAt),
    /<button/u,
    "the rail's head carries no controls",
  );
  // The strip drags the window; the controls in it do not. The custom property
  // inherits, so the no-drag has to be declared on the buttons themselves.
  assert.match(
    rendererCSS,
    /\.titlebar\s*\{\s*--wails-draggable:\s*drag;/u,
    "the title bar is the drag region",
  );
  assert.match(
    rendererCSS,
    /\.titlebar button\s*\{\s*--wails-draggable:\s*no-drag;/u,
    "a button that drags the window cannot be clicked",
  );
  // The left indent clears the inset traffic lights; the workspace fold stands
  // down whenever there is no run to project, wherever the button sits.
  assert.match(
    rendererCSS,
    /\.titlebar\s*\{[^}]*?padding:\s*0 var\(--sp-2\) 0 (7[0-9]|8[0-9])px;/u,
    "the first control starts right of the traffic lights",
  );
  assert.match(
    rendererCSS,
    /body:has\(#thread-panel\[hidden\]\)\s*#context-panel-button\s*\{\s*display:\s*none;/u,
    "the workspace fold stands down when there is nothing to show",
  );
  assert.doesNotMatch(rendererHTML, /id=["']sidebar-expand-button["']/u);
  assert.doesNotMatch(rendererSource, /sidebarExpandButton/u);
}

// --- Density is one number, or it is three sets of rules that drift ----------
//
// The mechanism only pays for itself if EVERY spacing step goes through the
// factor and nothing in the sheet states a spacing of its own. A single
// hard-coded padding is not a small inconsistency here — it is a row that
// stays put while everything around it moves.
{
  for (const step of [1, 2, 3, 4, 5, 6, 7]) {
    assert.match(
      rendererCSS,
      new RegExp(`--sp-${step}: calc\\(\\d+px \\* var\\(--density\\)\\);`, "u"),
      `spacing step ${step} must scale with the density factor`,
    );
  }
  assert.match(rendererCSS, /:root\[data-density="compact"\]\s*\{\s*--density:\s*0?\.\d+;/u);
  assert.match(rendererCSS, /:root\[data-density="comfortable"\]\s*\{\s*--density:\s*1\.\d+;/u);
  // Standard is the absence of the attribute, exactly as "system" is for the
  // theme. A third selector here would be a default that a shell which failed
  // to read the database could get wrong.
  assert.doesNotMatch(rendererCSS, /\[data-density="standard"\]/u);
  // Type and control heights sit outside it deliberately: shrinking text to
  // fit more rows in trades legibility for density, and a button under about
  // 24px stops being reliably hittable.
  for (const fixed of ["--fs-base", "--fs-read", "--h-control", "--h-control-sm"]) {
    const declared = rendererCSS.match(new RegExp(`${fixed}: ([^;]+);`, "u"));
    assert.ok(declared, `${fixed} must be declared`);
    assert.doesNotMatch(
      declared[1],
      /var\(--density\)/u,
      `${fixed} must not scale with density`,
    );
  }
}

// --- On an approval card, weight follows breadth -----------------------------
//
// The claim the class names alone cannot make: the two answers that settle
// just this call carry colour at equal weight, and the two that grant a tool
// beyond it are quiet. A permanent grant must not be the easiest thing on the
// card to press, and "yes" must not out-shout "no".
{
  const base = rendererCSS.match(/\n\.approval-button \{([\s\S]*?)\n\}/u);
  assert.ok(base, ".approval-button must be declared");
  assert.match(
    base[1],
    /color: hsl\(var\(--muted-foreground\)\);/u,
    "the default weight of an approval answer is quiet; colour is opted into",
  );
  assert.doesNotMatch(
    base[1],
    /var\(--primary\)|var\(--destructive\)/u,
    "a colour on the base rule would tint the broad grants too",
  );
  assert.match(
    rendererCSS,
    // [^}] and not [\s\S]: an unbounded wildcard walks straight past the
    // closing brace and finds the same declaration in the :hover rule below,
    // so the pin passes while the rule it names is gone. Mutation testing is
    // what caught that — the assertion read as specific and was not.
    /\.approval-button\.once \{[^}]*?color: hsl\(var\(--primary\)\);/u,
    "the narrowest yes is the one that carries the accent",
  );
  assert.match(
    rendererCSS,
    /\.approval-button\.deny \{[^}]*?color: hsl\(var\(--destructive\)\);/u,
    "and no is stated at the same weight as yes, not more quietly",
  );
  // Nothing gives the broad grants a colour of their own, by any route.
  assert.doesNotMatch(
    rendererCSS,
    /\.approval-button\.broad/u,
    "a rule for the broad grants exists only to make them louder; there is none",
  );
  // And none of the four is filled: an approval is a choice, not a primary
  // action, and a solid button is the interface having an opinion.
  assert.doesNotMatch(
    rendererCSS,
    /\.approval-button[^{]*\{[^}]*background: hsl\(var\(--primary\)\);/u,
    "no approval answer is a solid button",
  );
}

// --- A button that stopped being a button has to say so completely -----------
//
// The base rule centres a button's content, which is right for one holding a
// word and wrong for one holding a title over a metadata line. Changing
// `display` to grid does not undo it: justify-content: center centres the
// grid's single column exactly as happily as it centred a flex row, so every
// conversation title sat at its own left edge and the rail read ragged.
//
// This pins the pair, because either half alone is misleading — the base rule
// is not wrong, and the reset is meaningless without it.
{
  assert.match(
    rendererCSS,
    /^button \{[^}]*?justify-content: center;/mu,
    "the base button rule centres its content; the reset below exists because of it",
  );
  assert.match(
    rendererCSS,
    /\.thread-button \{[^}]*?justify-content: stretch;/u,
    "a thread row holds two stacked lines and must not inherit the centring",
  );
}

// --- Three habits that only hold if nothing is allowed to opt out ------------
//
// Each of these is one rule that every site has to actually use. The value is
// not in any single site — it is that a new one cannot get a different answer,
// which is exactly what a pin can enforce and a screenshot cannot.
{
  // A native window points with an arrow. The hand cursor said "hyperlink" on
  // every button in the app; the policy lives in one zero-specificity rule and
  // nothing may re-declare the web's answer beside it.
  assert.match(
    rendererCSS,
    /:where\(button, \[role="button"\], a\[href\], summary, label\[for\][^)]*\)\s*\{\s*cursor:\s*default;/u,
    "the cursor policy is stated once, for all controls",
  );
  assert.doesNotMatch(
    rendererCSS,
    /cursor:\s*pointer/u,
    "a control that re-declares the hand cursor puts the web back into the window",
  );

  // Reveal-on-hover, all three parts. opacity so revealing never reflows the
  // row; pointer-events because an invisible control is still hit-testable;
  // :focus-within because a control that only appears on hover is one a
  // keyboard can reach and never see.
  assert.match(
    rendererCSS,
    /\.reveal-on-hover\s*\{\s*opacity:\s*0;\s*pointer-events:\s*none;/u,
    "a hidden control must be invisible AND untouchable",
  );
  assert.match(
    rendererCSS,
    /\.reveal-on-hover-host:focus-within \.reveal-on-hover,/u,
    "the keyboard must be able to see what the pointer can",
  );
  assert.match(
    rendererCSS,
    /\.reveal-on-hover:focus-within,\s*\.reveal-on-hover\.revealed\s*\{\s*opacity:\s*1;\s*pointer-events:\s*auto;/u,
    "revealing restores both halves together",
  );
  // The three sites that used to spell this out for themselves. Each kept a
  // different subset of it, which is how the mechanism drifts.
  for (const gone of [
    /\.thread-item:hover \.thread-delete/u,
    /\.content-topbar:hover \.thread-title-row button/u,
    /\.message:hover \.message-actions/u,
  ]) {
    assert.doesNotMatch(rendererCSS, gone, "no site keeps a private copy of reveal-on-hover");
  }
  assert.match(rendererHTML, /id="content-topbar" class="content-topbar reveal-on-hover-host"/u);
  assert.match(rendererHTML, /id="rename-thread-button" class="ghost reveal-on-hover"/u);
  assert.match(rendererSource, /item\.className = "thread-item reveal-on-hover-host"/u);
  assert.match(rendererSource, /wrapper\.className = `message \$\{role\} reveal-on-hover-host`/u);
  // An armed Delete is waiting for a second click and must not vanish when the
  // pointer leaves the row it is on — it uses the mechanism's own escape hatch
  // rather than a rule of its own.
  assert.match(
    rendererSource,
    /del\.classList\.add\("armed", "revealed"\)/u,
    "an armed delete stays visible through the shared mechanism",
  );
  assert.match(rendererSource, /del\.classList\.remove\("armed", "revealed"\)/u);

  // Switching the theme rewrites every colour token at once. Without this the
  // window spends the transition duration as a wash of half-old colour.
  assert.match(
    rendererCSS,
    /:root\.theme-switching \*,[^}]*?\{\s*transition:\s*none\s*!important;/u,
    "a theme switch must not animate",
  );
  assert.doesNotMatch(
    rendererCSS,
    /:root\.theme-switching \*[\s\S]{0,120}animation:\s*none/u,
    "a running animation has nothing to do with the theme and would only jump if frozen",
  );
  assert.match(
    rendererSource,
    /root\.classList\.add\("theme-switching"\)/u,
    "the suppression is applied around the token rewrite, by the code that does it",
  );
}

// --- The main column's chrome occupies nothing when it has nothing to say -----
//
// Same technique, same reason: the welcome screen is centred in the whole
// column, not in what is left under a bar drawn for a conversation that is not
// open. The strip owes nobody a control in this state — every view control it
// used to carry in it lives in the title bar now, which is always on screen.
{
  assert.match(
    rendererCSS,
    /\.content:has\(#thread-panel\[hidden\]\)\s*\.content-topbar\s*\{\s*display:\s*none;/u,
    "with nothing open the chrome strip must take no height at all",
  );
  // The header that used to live inside the thread panel is gone: it was two
  // lines (title over metadata) inside a column that had a two-line rail head
  // above it. Both are one line now, and the parts of it live in the strip —
  // which is what lets the strip disappear with them.
  assert.doesNotMatch(rendererCSS, /\.thread-panel header/u);
  const topbarAt = rendererHTML.indexOf('id="content-topbar"');
  const panelAt = rendererHTML.indexOf('id="thread-panel"');
  assert.ok(topbarAt > 0 && panelAt > topbarAt, "the chrome strip precedes the thread panel");
  const topbarHTML = rendererHTML.slice(topbarAt, panelAt);
  for (const id of [
    "thread-title-row",
    "thread-title",
    "thread-meta",
    "rename-thread-button",
    "export-thread-button",
    "rename-thread-form",
    "turn-state",
  ]) {
    assert.match(
      topbarHTML,
      new RegExp(`id=["']${id}["']`, "u"),
      `${id} belongs to the main column's one line of chrome`,
    );
  }
}

// The conversation list keeps itself fresh: every mutation repaints it, and the
// one state where asking again is the answer is an error, which offers Retry on
// the status strip and calls the same refresh(). A bare ↻ next to the list
// heading was a control with no label and no job.
assert.doesNotMatch(rendererHTML, /id=["']refresh-button["']/u);
assert.doesNotMatch(rendererSource, /refreshButton/u);

// The AGPL offer of source, and the version of what is running. Both used to
// be a two-line footer under the rail's identity row; both are in Settings ›
// About now, which is where a thing you look up once belongs. The offer has to
// keep existing wherever it lives, so it is pinned to the settings dialog
// rather than to the document as a whole — "somewhere in index.html" would
// have passed while the rail still carried it.
{
  const overlayAt = rendererHTML.indexOf('id="settings-overlay"');
  const shellAt = rendererHTML.indexOf('<main class="shell">');
  assert.ok(overlayAt > 0 && shellAt > overlayAt, "the settings dialog precedes the shell");
  const settingsHTML = rendererHTML.slice(overlayAt, shellAt);
  assert.match(settingsHTML, /id=["']source-code-link["']/u);
  assert.match(settingsHTML, /href=["']https:\/\/github\.com\/jonnyquan\/workmax["']/u);
  assert.match(settingsHTML, /Source code\s*·\s*AGPL-3\.0/u);
  assert.match(settingsHTML, /id=["']runtime-label["']/u);
  // The rail ends at the identity row.
  assert.doesNotMatch(rendererHTML, /sidebar-footer/u);
  assert.doesNotMatch(rendererCSS, /\.sidebar-footer/u);
  // And the identity popover it used to sit under is gone with it: the list,
  // the rename and the delete-everything button live in Settings › Account.
  assert.doesNotMatch(rendererHTML, /id=["']local-account-panel["']/u);
  assert.doesNotMatch(rendererSource, /localAccountPanel/u);
  assert.doesNotMatch(rendererCSS, /\.account-popover/u);
}

assert.doesNotMatch(rendererSource, /["']\/auth\/start["']/u);
assert.doesNotMatch(rendererSource, /["']\/auth\/login-transaction(?:\/password)?["']/u);
assert.doesNotMatch(rendererSource, /openOAuthWindow|authorize_url|auth_port|oauth\/callback/u);
assert.doesNotMatch(
  rendererSource,
  /\/api\/v1\/desktop\/identity\/login-transactions|transaction_secret|transactionSecret|exchange_token|exchangeToken|DesktopLogin|DesktopExchange|authorization_code|redirect_location/u
);
assert.doesNotMatch(rendererSource, /console\./u);
// The renderer keeps nothing in web storage, in any spelling.
//
// The appearance preference was the last holdout and it is gone: localStorage
// is scoped to an origin, this app's UI origin binds a fresh port every launch,
// so the theme was being written to a store no later launch could read. It
// lives in SQLite behind the sidecar now, and the document arrives with
// data-theme already on <html>. Nothing else may drift back in here — the
// sidecar is the only thing on this machine whose identity survives a restart.
{
  // Comment lines are excluded: the rule is about what the code does, not
  // about whether a comment may name the thing it explains.
  const codeLines = rendererSource
    .split("\n")
    .filter((line) => !/^\s*(?:\/\/|\/\*|\*)/u.test(line));
  const storageUses = codeLines.filter((line) =>
    /\b(?:localStorage|sessionStorage|indexedDB)\b/u.test(line),
  );
  assert.deepEqual(storageUses, [], "the renderer must keep no state in web storage");
}
// The Markdown renderer and the syntax highlighter both build DOM out of
// createElement and createTextNode, which is what makes "model output cannot
// become markup" a structural property rather than a matter of escaping
// correctly. innerHTML is tolerated for exactly one thing — clearing a list to
// the empty string, which cannot parse anything — and any other use, or any use
// of the two APIs that have no safe form at all, fails here.
{
  const codeLines = rendererSource
    .split("\n")
    .filter((line) => !/^\s*(?:\/\/|\/\*|\*)/u.test(line));
  assert.ok(
    !codeLines.some((line) => /outerHTML|insertAdjacentHTML/u.test(line)),
    "outerHTML and insertAdjacentHTML have no safe form here",
  );
  for (const line of codeLines) {
    for (const match of line.matchAll(/innerHTML\s*=\s*(.*)$/gu)) {
      assert.equal(
        match[1].trim(),
        '"";',
        `innerHTML may only be assigned "" (clearing), got: ${line.trim()}`,
      );
    }
  }
}
for (const method of [
  "beginLogin",
  "loginStatus",
  "submitLoginPassword",
  "cancelLogin",
]) {
  assert.match(rendererSource, new RegExp(`auth\\.${method}\\b`, "u"));
}
for (const id of [
  "login-form",
  "login-email",
  "login-password",
  "login-submit-button",
  "login-cancel-button",
  "new-thread-button",
  "new-thread-form",
  "new-thread-name",
  "new-thread-mode",
  "new-thread-error",
  "new-thread-submit-button",
  "new-thread-cancel-button",
  "empty-title",
  "empty-description",
  "empty-new-thread-button",
  "chat-form",
  "agent-mode",
  "composer-status",
  "chat-input",
  "stop-button",
  "send-button",
  "turn-state",
  "message-viewport",
  "turn-recovery-card",
  "turn-recovery-description",
  "turn-recovery-prompt",
  "turn-recovery-feedback",
  "turn-recovery-resume-button",
  "turn-recovery-dismiss-button",
]) {
  assert.match(rendererHTML, new RegExp(`id=["']${id}["']`, "u"));
}
assert.match(rendererHTML, /id=["']login-password["'][\s\S]*?type=["']password["']/u);

class FakeClassList {
  constructor() {
    this.values = new Set();
  }

  toggle(name, force) {
    // Real DOM semantics: one-argument toggle flips; the force form sets.
    const shouldAdd = force === undefined ? !this.values.has(name) : Boolean(force);
    if (shouldAdd) {
      this.values.add(name);
    } else {
      this.values.delete(name);
    }
    return shouldAdd;
  }

  add(name) {
    this.values.add(name);
  }

  remove(name) {
    this.values.delete(name);
  }

  contains(name) {
    return this.values.has(name);
  }
}

class FakeElement {
  constructor(tagName = "div") {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.attributes = new Map();
    this.listeners = new Map();
    this.classList = new FakeClassList();
    this.dataset = {};
    this.hidden = false;
    this._textContent = "";
    this._className = "";
    this.type = "";
    this.value = "";
    this.disabled = false;
    this.focused = false;
    this.parentNode = null;
    this.scrollTop = 0;
    this.style = {};
  }

  set className(value) {
    this._className = value;
    this.classList.values = new Set(String(value).split(/\s+/).filter(Boolean));
  }

  get className() {
    return this._className;
  }

  set textContent(value) {
    this._textContent = String(value);
    this.children = [];
  }

  get textContent() {
    return this._textContent + this.children.map((child) => child.textContent).join("");
  }

  set innerHTML(_value) {
    this.textContent = "";
  }

  append(...nodes) {
    for (const node of nodes) {
      this.appendChild(node);
    }
  }

  appendChild(node) {
    node.parentNode = this;
    this.children.push(node);
    return node;
  }

  insertBefore(node, reference) {
    node.parentNode = this;
    const at = this.children.indexOf(reference);
    if (at < 0) this.children.push(node);
    else this.children.splice(at, 0, node);
    return node;
  }

  remove() {
    if (!this.parentNode) return;
    this.parentNode.children = this.parentNode.children.filter(
      (child) => child !== this
    );
    this.parentNode = null;
  }

  replaceChild(next, previous) {
    const at = this.children.indexOf(previous);
    if (at < 0) return previous;
    next.parentNode = this;
    this.children[at] = next;
    previous.parentNode = null;
    return previous;
  }

  get nextSibling() {
    const siblings = this.parentNode?.children ?? [];
    const at = siblings.indexOf(this);
    return at >= 0 && at + 1 < siblings.length ? siblings[at + 1] : null;
  }

  addEventListener(type, handler) {
    this.listeners.set(type, handler);
  }

  click() {
    if (this.disabled) {
      return;
    }
    const handler = this.listeners.get("click");
    if (handler) {
      handler({ preventDefault() {} });
    }
  }

  submit() {
    const handler = this.listeners.get("submit");
    if (handler) {
      handler({ preventDefault() {} });
    }
  }

  dispatch(type, init = {}) {
    const handler = this.listeners.get(type);
    if (!handler) return;
    handler({
      ...init,
      key: init.key,
      metaKey: init.metaKey ?? false,
      ctrlKey: init.ctrlKey ?? false,
      repeat: init.repeat ?? false,
      preventDefault: init.preventDefault ?? (() => {}),
      // The stub has no bubbling, so an element handler here is already the
      // only one that runs. It is provided anyway because the code under test
      // calls it: a handler that stops an event from reaching the document
      // must be able to say so without the stub throwing at it.
      stopPropagation: init.stopPropagation ?? (() => {}),
    });
  }

  get scrollHeight() {
    // Each row is 100 tall. scrollContent exists because the element that
    // scrolls is not always the element with the rows in it: #message-viewport
    // scrolls #message-list, a relationship the flat element map cannot
    // express, and without it the viewport's height never changes and the
    // transcript's scroll anchoring cannot be observed at all.
    return (this.scrollContent ?? this).children.length * 100;
  }

  // A stable viewport height so the sticky-scroll math is deterministic in
  // the stub: near-bottom means scrollTop >= children*100 - 400 - 48.
  get clientHeight() {
    return 400;
  }

  focus() {
    this.focused = true;
  }

  select() {
    this.selected = true;
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  getAttribute(name) {
    return this.attributes.get(name) ?? null;
  }

  removeAttribute(name) {
    this.attributes.delete(name);
  }
}

// The element set is derived from index.html rather than listed here.
//
// It used to be a hardcoded array, and it drifted: twenty elements added by
// later milestones (model settings, attachments) were never added to the list,
// so renderer.js threw on a null element and this whole behavior suite has
// been failing rather than checking anything. Reading the real markup means
// the stub cannot fall behind what ships — a new element in index.html simply
// exists here too.
//
// Tag name and initial hidden state come from the markup for the same reason:
// guessing them from the id ("...-button" means a button) is another place for
// the two to disagree.
function parseRendererElements(html) {
  const elements = new Map();
  for (const match of html.matchAll(/<([a-zA-Z][a-zA-Z0-9-]*)\s([^>]*)>/gu)) {
    const [, tagName, attributes] = match;
    const idMatch = /\bid="([^"]+)"/u.exec(attributes);
    if (!idMatch) continue;
    elements.set(idMatch[1], {
      tagName: tagName.toLowerCase(),
      hidden: /\bhidden\b/u.test(attributes),
    });
  }
  if (elements.size === 0) {
    throw new Error("no id-bearing elements found in index.html; the parse is wrong, not the markup");
  }
  return elements;
}

class FakeDocument {
  constructor(rendererHtml) {
    this.byId = new Map();
    this.listeners = new Map();
    // <html>. The appearance preference is an attribute on it — written by the
    // shell into the served markup, before a single line of renderer.js runs —
    // so it has to exist, and be settable, from the start.
    this.documentElement = new FakeElement("html");
    // Operation counters, for the performance gate: a per-token selector walk
    // or full-text reset is invisible to a correctness assertion and shows up
    // here as a count that scales with the delta count.
    this.querySelectorCalls = 0;
    this.appendDataCalls = 0;
    for (const [id, spec] of parseRendererElements(rendererHtml)) {
      const element = new FakeElement(spec.tagName);
      element.hidden = spec.hidden;
      this.byId.set(id, element);
    }
    const viewport = this.byId.get("message-viewport");
    const list = this.byId.get("message-list");
    if (viewport && list) viewport.scrollContent = list;
  }

  // Document-level listeners, for global keyboard shortcuts. dispatchKey is
  // the test's way of typing at the app rather than at a specific element.
  addEventListener(type, handler) {
    this.listeners.set(type, handler);
  }

  dispatchKey(init) {
    const handler = this.listeners.get("keydown");
    if (!handler) return;
    handler({
      key: init.key,
      metaKey: init.metaKey ?? false,
      ctrlKey: init.ctrlKey ?? false,
      repeat: init.repeat ?? false,
      preventDefault: init.preventDefault ?? (() => {}),
      target: init.target ?? null,
    });
  }

  querySelector(selector) {
    this.querySelectorCalls += 1;
    if (!selector.startsWith("#")) {
      throw new Error(`unsupported selector in bundled renderer test: ${selector}`);
    }
    return this.byId.get(selector.slice(1)) ?? null;
  }

  createElement(tagName) {
    return new FakeElement(tagName);
  }

  // Text nodes are how the Markdown renderer puts model output on the page —
  // never as markup. The stub keeps them distinguishable from elements so a
  // test can assert that a would-be tag really did arrive as text.
  createTextNode(data) {
    const doc = this;
    return {
      nodeType: 3,
      tagName: "#text",
      children: [],
      classList: new FakeClassList(),
      parentNode: null,
      textContent: String(data),
      // Real Text semantics: append without rebuilding, which is the whole
      // point of the streaming batcher — and counted, so the gate can assert
      // one append per flushed frame rather than one per delta.
      appendData(chunk) {
        doc.appendDataCalls += 1;
        this.textContent += String(chunk);
      },
    };
  }
}

function response(body, init = {}) {
  return {
    ok: init.ok ?? true,
    status: init.status ?? 200,
    async json() {
      return body;
    },
  };
}

function typedSuccess(data, status = 200) {
  return {
    ok: true,
    status,
    statusText: status === 200 ? "OK" : "Success",
    headers: { "content-type": "application/json" },
    data,
  };
}

function typedFailure(status, error) {
  return {
    ok: false,
    status,
    statusText: status === 409 ? "Conflict" : "Error",
    headers: { "content-type": "application/json" },
    error,
  };
}

function pptCatalog() {
  return {
    items: [
      {
        agentMode: "ppt",
        name: "Presentation",
        description: "Create and refine presentation decks.",
        version: "2.1.0",
        hasQuestionForm: true,
        hasDirectionsFallback: true,
        hasPostScripts: true,
        labelKey: "skills.ppt.name",
        descriptionKey: "skills.ppt.description",
      },
    ],
    count: 1,
    allowed_modes: ["ppt"],
  };
}

function pptCatalogWithReplayMode() {
  const catalog = pptCatalog();
  catalog.items.push({
    ...catalog.items[0],
    agentMode: "ppt_revised",
    name: "Presentation revised",
    labelKey: "skills.ppt.revised.name",
    descriptionKey: "skills.ppt.revised.description",
  });
  catalog.count = 2;
  catalog.allowed_modes.push("ppt_revised");
  return catalog;
}

function thread(uuid, name, messageCount = 1) {
  return {
    uuid,
    name,
    agent_mode: "ppt",
    message_count: messageCount,
    updated_at: "2026-05-21T00:00:00Z",
  };
}

function createdThread(uuid, name, agentMode = "ppt") {
  return {
    uuid,
    name,
    agent_mode: agentMode,
    message_count: 0,
    updated_at: "2026-08-06T00:00:00Z",
    cloud_sync_state: "synced",
  };
}

function recoverableTurn(overrides = {}) {
  return {
    turn_uuid: "123e4567-e89b-42d3-a456-426614174000",
    thread_uuid: "thread-agent",
    user_text: "  Resume the quarterly deck  ",
    chat_mode: "ppt",
    state: "interrupted",
    last_error_kind: "transport_error",
    updated_at: "2026-08-06T00:00:00Z",
    ...overrides,
  };
}

function message(uuid, userText, aiText, streamingState = "complete", provenance = {}) {
  return {
    uuid,
    user_text: userText,
    ai_text: aiText,
    streaming_state: streamingState,
    // Migration 0013. Absent on every row written before it, which is why the
    // default here is the empty pair rather than a plausible-looking model.
    agent_engine: provenance.engine ?? "",
    agent_model: provenance.model ?? "",
    agent_mind: provenance.mind ?? "",
    created_at: "2026-05-21T00:00:00Z",
    updated_at: "2026-05-21T00:00:00Z",
  };
}

async function settle() {
  await new Promise((resolve) => setImmediate(resolve));
  await new Promise((resolve) => setImmediate(resolve));
}

function walk(node, predicate, out = []) {
  if (predicate(node)) {
    out.push(node);
  }
  for (const child of node.children ?? []) {
    walk(child, predicate, out);
  }
  return out;
}

async function runRenderer(mockBridge, mockDesktopBridge, options = {}) {
  const document = new FakeDocument(rendererHTML);
  // What the shell wrote into <html> before the page was handed over. Absent
  // by default, which is what "follow the system" looks like on the wire —
  // and what every other test in this file runs against.
  if (options.theme) document.documentElement.setAttribute("data-theme", options.theme);
  let uuidSequence = 0;
  const crypto = {
    randomUUID() {
      uuidSequence += 1;
      return `00000000-0000-4000-8000-${String(uuidSequence).padStart(12, "0")}`;
    },
  };
  const context = {
    console,
    crypto,
    Date,
    URL,
    document,
    setTimeout: (handler) => setImmediate(handler),
    clearTimeout: () => {},
    window: {
      workmaxLocal: mockBridge,
      desktopBridge: mockDesktopBridge,
    },
  };
  // Absent unless a test asks for it: the renderer must degrade to no copy
  // affordance where there is no clipboard, and most tests run in exactly that
  // shape, so leaving it out keeps them honest about it.
  if (options.clipboard) context.navigator = { clipboard: options.clipboard };
  // Same reasoning for storage — see FakeStorage.
  if (options.storage) context.localStorage = options.storage;
  // Contextifying makes every sandbox property a real global inside the VM, so
  // the renderer's globalThis.localStorage resolves exactly as it would in a
  // webview — including to undefined when no storage was installed.
  vm.createContext(context);

  // One module graph per run, instantiated fresh in this context. Module
  // instances are per-context, so each test gets its own copies of `state` and
  // every other module-level binding — the isolation the old
  // one-script-per-context shape gave for free.
  const graph = new Map();
  const instantiate = (file) => {
    const resolved = path.resolve(file);
    const existing = graph.get(resolved);
    if (existing) return existing;
    const created = new vm.SourceTextModule(fs.readFileSync(resolved, "utf8"), {
      context,
      identifier: resolved,
    });
    graph.set(resolved, created);
    return created;
  };
  const entry = instantiate(rendererPath);
  await entry.link((specifier, referencing) =>
    instantiate(path.resolve(path.dirname(referencing.identifier), specifier)),
  );
  await entry.evaluate();

  // The modules were one file until recently and every name in them was a
  // global, so a flat view of the graph is the faithful successor to reaching
  // into that global scope — and the collision check keeps it honest, because
  // two modules declaring the same name is now a real possibility that a flat
  // view would otherwise hide.
  const ns = {};
  for (const module of graph.values()) {
    for (const name of Object.keys(module.namespace)) {
      assert.ok(
        !Object.prototype.hasOwnProperty.call(ns, name),
        `two renderer modules both export ${name}`,
      );
      Object.defineProperty(ns, name, {
        enumerable: true,
        get: () => module.namespace[name],
      });
    }
  }
  await settle();
  return { context, document, ns };
}

async function testMissingBridge() {
  const { document, ns } = await runRenderer(undefined);
  assert.match(
    document.byId.get("status-card").textContent,
    /must run inside WorkMax Desktop/
  );
  assert.equal(document.byId.get("status-card").classList.contains("error"), true);
}

async function testAuthenticatedCacheRead() {
  const calls = [];
  const bridge = {
    sidecarVersion: "sidecar-test",
    appVersion: "app-test",
    async fetch(pathname) {
      calls.push(pathname);
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          items: [
            {
              uuid: "thread one",
              name: "Storyboard draft",
              agent_mode: "ppt",
              message_count: 2,
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      if (pathname === "/agent/threads/thread%20one/messages") {
        return response({
          items: [
            {
              uuid: "msg one",
              user_text: "make a shot list",
              ai_text: "cached assistant answer",
              streaming_state: "complete",
              created_at: "2026-05-21T00:00:00Z",
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document, ns } = await runRenderer(bridge);
  assert.deepEqual(calls, ["/auth/status", "/agent/threads?include_paused=true"]);
  assert.match(document.byId.get("runtime-label").textContent, /sidecar sidecar-test · app app-test/);
  assert.equal(document.byId.get("local-account-connect").hidden, true);
  assert.match(document.byId.get("thread-list").textContent, /Storyboard draft/);

  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button")
  )[0];
  assert.ok(threadButton, "expected a thread button");
  threadButton.click();
  await settle();

  assert.equal(calls.at(-1), "/agent/threads/thread%20one/messages");
  assert.equal(document.byId.get("empty-state").hidden, true);
  assert.equal(document.byId.get("thread-panel").hidden, false);
  assert.match(document.byId.get("message-list").textContent, /make a shot list/);
  assert.match(document.byId.get("message-list").textContent, /cached assistant answer/);
  {
    const times = walk(
      document.byId.get("message-list"),
      (n) => n.classList?.contains("message-time"),
    );
    assert.equal(times.length, 2, "cached messages must show their stored times");
    for (const t of times) {
      assert.notEqual(t.textContent, "", "a timestamp must render as text, not sit empty");
    }
  }
  assert.equal(document.byId.get("chat-input").disabled, true);
  assert.equal(document.byId.get("send-button").disabled, true);
  assert.match(document.byId.get("composer-status").textContent, /streaming is unavailable/i);
}

async function testUnauthenticatedLogin() {
  const calls = [];
  const beginCalls = [];
  const statusCalls = [];
  const passwordCalls = [];
  let authenticated = false;
  let rendererDocument;
  const bridge = {
    async fetch(pathname, init = {}) {
      calls.push([pathname, init.method ?? "GET"]);
      if (pathname === "/auth/status") {
        return response({
          state: authenticated ? "authenticated" : "unauthenticated",
          tier: authenticated ? "pro" : undefined,
          updated_at: "2026-05-21T00:00:00Z",
        });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    auth: {
      async beginLogin(...args) {
        beginCalls.push(args);
        return { state: "awaiting_password" };
      },
      async loginStatus(...args) {
        statusCalls.push(args);
        return { state: "idle" };
      },
      async submitLoginPassword(input) {
        assert.equal(
          rendererDocument.byId.get("login-password").value,
          "",
          "password DOM value must be cleared before the privileged IPC begins"
        );
        passwordCalls.push(input);
        authenticated = true;
        return { state: "authenticated" };
      },
      async cancelLogin() {
        throw new Error("cancelLogin should not be called during successful sign-in");
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  rendererDocument = document;
  assert.equal(document.byId.get("local-account-connect").hidden, false);
  assert.equal(document.byId.get("login-form").hidden, true);
  assert.match(
    document.byId.get("status-card").textContent,
    /Working on this machine\. Connect an account or a local model/,
    "no account connected is a state of a usable app, not a wall in front of it",
  );
  assert.deepEqual(statusCalls, [[]]);

  document.byId.get("local-account-connect").click();
  await settle();
  assert.deepEqual(beginCalls, [[]]);
  assert.equal(document.byId.get("local-account-connect").hidden, true);
  assert.equal(document.byId.get("login-form").hidden, false);
  assert.match(document.byId.get("status-card").textContent, /email and password/i);

  document.byId.get("login-email").value = "  writer@example.com  ";
  document.byId.get("login-password").value = "do-not-persist-this";
  document.byId.get("login-form").submit();
  assert.equal(document.byId.get("login-password").value, "");
  await settle();
  await settle();

  assert.equal(passwordCalls.length, 1);
  assert.equal(passwordCalls[0].email, "writer@example.com");
  assert.equal(passwordCalls[0].password, "do-not-persist-this");
  assert.equal(document.byId.get("login-password").value, "");
  assert.doesNotMatch(
    JSON.stringify(ns.state),
    /writer@example\.com|do-not-persist-this/u
  );
  assert.equal(document.byId.get("login-form").hidden, true);
  assert.equal(calls.some(([pathname]) => pathname.startsWith("/auth/login-transaction")), false);
  assert.deepEqual(calls.at(-1), ["/agent/threads?include_paused=true", "GET"]);
  assert.match(document.byId.get("status-card").textContent, /Authenticated/);
}

async function testResumesAndCancelsPasswordLogin() {
  let cancelCalls = 0;
  let rendererDocument;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    auth: {
      async beginLogin() {
        throw new Error("beginLogin should not replace the resumable transaction");
      },
      async loginStatus() {
        return { state: "awaiting_password" };
      },
      async submitLoginPassword() {
        throw new Error("submitLoginPassword should not run in the cancellation test");
      },
      async cancelLogin() {
        cancelCalls += 1;
        assert.equal(rendererDocument.byId.get("login-password").value, "");
        return { state: "idle" };
      },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  rendererDocument = document;
  assert.equal(document.byId.get("login-form").hidden, false);
  document.byId.get("login-password").value = "must-be-cleared-on-cancel";
  document.byId.get("login-cancel-button").click();
  await settle();

  assert.equal(cancelCalls, 1);
  assert.equal(document.byId.get("login-password").value, "");
  assert.equal(document.byId.get("login-form").hidden, true);
  assert.match(document.byId.get("status-card").textContent, /Sign-in was canceled/);
}

async function testInvalidCredentialsStayRetryableAndClearPassword() {
  let passwordCalls = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    auth: {
      async beginLogin() {
        return { state: "awaiting_password" };
      },
      async loginStatus() {
        return { state: "idle" };
      },
      async submitLoginPassword() {
        passwordCalls += 1;
        return { state: "awaiting_password", error: "invalid_credentials" };
      },
      async cancelLogin() {
        return { state: "idle" };
      },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  document.byId.get("local-account-connect").click();
  await settle();
  document.byId.get("login-email").value = "writer@example.com";
  document.byId.get("login-password").value = "wrong-password";
  document.byId.get("login-form").submit();
  await settle();

  assert.equal(passwordCalls, 1);
  assert.equal(document.byId.get("login-form").hidden, false);
  assert.equal(document.byId.get("login-password").value, "");
  assert.equal(document.byId.get("login-submit-button").disabled, false);
  assert.match(document.byId.get("status-card").textContent, /email or password is incorrect/i);
  assert.doesNotMatch(document.byId.get("status-card").textContent, /invalid_credentials/u);
}

async function testCancelFencesLatePasswordCompletion() {
  let resolvePasswordSubmission;
  const pendingPasswordSubmission = new Promise((resolve) => {
    resolvePasswordSubmission = resolve;
  });
  const calls = [];
  const bridge = {
    async fetch(pathname) {
      calls.push(pathname);
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      // Boot reads local history with or without an account: identity always
      // resolves, so the workbench is not gated on signing in.
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    auth: {
      async beginLogin() {
        return { state: "awaiting_password" };
      },
      async loginStatus() {
        return { state: "idle" };
      },
      async submitLoginPassword() {
        return pendingPasswordSubmission;
      },
      async cancelLogin() {
        return { state: "idle" };
      },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  document.byId.get("local-account-connect").click();
  await settle();
  document.byId.get("login-email").value = "writer@example.com";
  document.byId.get("login-password").value = "late-password";
  document.byId.get("login-form").submit();
  document.byId.get("login-cancel-button").click();
  await settle();

  resolvePasswordSubmission({ state: "authenticated" });
  await settle();
  assert.equal(document.byId.get("login-password").value, "");
  assert.equal(document.byId.get("login-form").hidden, true);
  assert.match(document.byId.get("status-card").textContent, /Sign-in was canceled/);
  // The boot pair and nothing else: a canceled sign-in must not re-read the
  // session or replay anything.
  assert.deepEqual(calls, ["/auth/status", "/agent/threads?include_paused=true"]);
}

async function testAmbiguousPasswordResponseReconcilesSessionWithoutReplay() {
  const calls = [];
  let authenticated = false;
  let passwordCalls = 0;
  const bridge = {
    async fetch(pathname) {
      calls.push(pathname);
      if (pathname === "/auth/status") {
        return response({
          state: authenticated ? "authenticated" : "unauthenticated",
          tier: authenticated ? "pro" : undefined,
          updated_at: "2026-05-21T00:00:00Z",
        });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    auth: {
      async beginLogin() {
        return { state: "awaiting_password" };
      },
      async loginStatus() {
        return { state: "idle" };
      },
      async submitLoginPassword() {
        passwordCalls += 1;
        authenticated = true;
        // Electron Main collapses a lost/malformed Sidecar response to this
        // closed result instead of exposing transport details to Renderer.
        return { state: "idle", error: "unavailable" };
      },
      async cancelLogin() {
        return { state: "idle" };
      },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  document.byId.get("local-account-connect").click();
  await settle();
  document.byId.get("login-email").value = "writer@example.com";
  document.byId.get("login-password").value = "ambiguous-password";
  document.byId.get("login-form").submit();
  await settle();
  await settle();

  assert.equal(passwordCalls, 1, "an ambiguous password result must never be replayed");
  assert.equal(document.byId.get("login-password").value, "");
  assert.equal(document.byId.get("login-form").hidden, true);
  assert.match(document.byId.get("status-card").textContent, /Authenticated/);
  // Boot reads local history as this machine's identity; the reconcile then
  // re-reads the session once and loads the account's history. What matters
  // is that the password was not replayed — asserted above — and that the
  // session is read exactly twice.
  assert.deepEqual(calls, [
    "/auth/status",
    "/agent/threads?include_paused=true",
    "/auth/status",
    "/agent/threads?include_paused=true",
  ]);
}

async function testRejectsMalformedAuthStatus() {
  for (const payload of [
    { state: "admin", tier: "pro", updated_at: "2026-05-21T00:00:00Z" },
    { state: "authenticated", tier: "pro" },
    { state: "authenticated", tier: 123, updated_at: "2026-05-21T00:00:00Z" },
    { state: "authenticated", user_id: 123, updated_at: "2026-05-21T00:00:00Z" },
  ]) {
    const bridge = {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          return response(payload);
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };

    const { document, ns } = await runRenderer(bridge);
    assert.match(document.byId.get("status-card").textContent, /Malformed \/auth\/status response/);
    assert.equal(document.byId.get("status-card").classList.contains("error"), true);
  }
}

async function testRejectsMalformedThreadList() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          items: [
            {
              uuid: " bad-thread",
              name: "Bad thread",
              agent_mode: "ppt",
              message_count: 0,
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document, ns } = await runRenderer(bridge);
  assert.match(document.byId.get("status-card").textContent, /Malformed \/agent\/threads response/);
  assert.equal(document.byId.get("status-card").classList.contains("error"), true);
}

async function testRejectsMalformedThreadCountAndTimestamp() {
  for (const item of [
    {
      uuid: "thread one",
      name: "Bad count",
      agent_mode: "ppt",
      message_count: -1,
      updated_at: "2026-05-21T00:00:00Z",
    },
    {
      uuid: "thread one",
      name: "Bad timestamp",
      agent_mode: "ppt",
      message_count: 1,
      updated_at: "not-a-date",
    },
  ]) {
    const bridge = {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=true") {
          return response({ items: [item] });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };

    const { document, ns } = await runRenderer(bridge);
    assert.match(document.byId.get("status-card").textContent, /Malformed \/agent\/threads response/);
    assert.equal(document.byId.get("status-card").classList.contains("error"), true);
  }
}

async function testRejectsMalformedMessages() {
  const calls = [];
  const bridge = {
    async fetch(pathname) {
      calls.push(pathname);
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          items: [
            {
              uuid: "thread one",
              name: "Storyboard draft",
              agent_mode: "ppt",
              message_count: 1,
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      if (pathname === "/agent/threads/thread%20one/messages") {
        return response({
          items: [
            {
              uuid: "msg-one\n",
              user_text: "make a shot list",
              ai_text: "cached assistant answer",
              streaming_state: "complete",
              created_at: "2026-05-21T00:00:00Z",
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document, ns } = await runRenderer(bridge);
  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button")
  )[0];
  assert.ok(threadButton, "expected a thread button");
  threadButton.click();
  await settle();

  assert.equal(calls.at(-1), "/agent/threads/thread%20one/messages");
  assert.match(
    document.byId.get("status-card").textContent,
    /Malformed \/agent\/threads\/:uuid\/messages response/
  );
  assert.equal(document.byId.get("status-card").classList.contains("error"), true);
}

async function testRejectsMalformedMessageTimestamps() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          items: [
            {
              uuid: "thread one",
              name: "Storyboard draft",
              agent_mode: "ppt",
              message_count: 1,
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      if (pathname === "/agent/threads/thread%20one/messages") {
        return response({
          items: [
            {
              uuid: "msg-one",
              user_text: "make a shot list",
              ai_text: "cached assistant answer",
              streaming_state: "complete",
              created_at: "bad-date",
              updated_at: "2026-05-21T00:00:00Z",
            },
          ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document, ns } = await runRenderer(bridge);
  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button")
  )[0];
  assert.ok(threadButton, "expected a thread button");
  threadButton.click();
  await settle();

  assert.match(
    document.byId.get("status-card").textContent,
    /Malformed \/agent\/threads\/:uuid\/messages response/
  );
  assert.equal(document.byId.get("status-card").classList.contains("error"), true);
}

async function testRejectsMalformedLoginTransactionResult() {
  for (const payload of [
    { state: "pending" },
    { state: "awaiting_password", error: "private-error-must-not-cross" },
    {
      state: "awaiting_password",
      redirect_location: "private-location-must-not-cross",
    },
    { state: "idle", error: "canceled", extra: "private-extra-must-not-cross" },
  ]) {
    const calls = [];
    const bridge = {
      async fetch(pathname) {
        calls.push(pathname);
        if (pathname === "/auth/status") {
          return response({ state: "unauthenticated", updated_at: "2026-05-21T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=true") {
          return response({ items: [] });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };
    const desktopBridge = {
      auth: {
        async beginLogin() {
          return payload;
        },
        async loginStatus() {
          return { state: "idle" };
        },
        async submitLoginPassword() {
          throw new Error("submitLoginPassword should not be called for malformed begin result");
        },
        async cancelLogin() {
          throw new Error("cancelLogin should not be called for malformed begin result");
        },
      },
    };

    const { document, ns } = await runRenderer(bridge, desktopBridge);
    document.byId.get("local-account-connect").click();
    await settle();

    assert.match(document.byId.get("status-card").textContent, /temporarily unavailable/i);
    assert.doesNotMatch(
      document.byId.get("status-card").textContent,
      /private-error|private-location|private-extra/u
    );
    assert.equal(document.byId.get("status-card").classList.contains("error"), true);
    // The boot pair only: a malformed begin result must not send the app
    // looking for a session it does not have.
    assert.deepEqual(calls, ["/auth/status", "/agent/threads?include_paused=true"]);
  }
}

async function testRedactsErrorStatusMessages() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        throw new Error(
          "Authorization: Bearer bearer-secret Basic bare-basic-secret https://user:pass@example.com/path?refresh_token=refresh-secret X-Local-Token=local-secret client_secret=client-secret password=password-secret apikey=api-secret secret=generic-secret"
        );
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document, ns } = await runRenderer(bridge);
  const status = document.byId.get("status-card").textContent;
  assert.match(status, /\[REDACTED\]/);
  assert.match(status, /Basic \[REDACTED\]/);
  for (const secret of [
    "bearer-secret",
    "bare-basic-secret",
    "user:pass",
    "refresh-secret",
    "local-secret",
    "client-secret",
    "password-secret",
    "api-secret",
    "generic-secret",
  ]) {
    assert.doesNotMatch(status, new RegExp(secret.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  assert.equal(document.byId.get("status-card").classList.contains("error"), true);
}

async function testCachedStreamingStatesRenderPartialAndRejectUnknown() {
  const validBridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("partial-thread", "Recovered responses", 2)] });
      }
      if (pathname === "/agent/threads/partial-thread/messages") {
        return response({
          items: [
            message("partial-message", "Partial prompt", "Interrupted answer", "partial"),
            message("streaming-message", "Streaming prompt", "", "streaming"),
          ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document, ns } = await runRenderer(validBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const partialAssistants = walk(
    document.byId.get("message-list"),
    (node) =>
      node.tagName === "ARTICLE" &&
      node.classList.contains("assistant") &&
      node.classList.contains("partial")
  );
  assert.equal(partialAssistants.length, 2);
  assert.match(document.byId.get("message-list").textContent, /Interrupted answer/);
  assert.match(document.byId.get("message-list").textContent, /Response interrupted/);

  const invalidBridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("invalid-state-thread", "Invalid state")] });
      }
      if (pathname === "/agent/threads/invalid-state-thread/messages") {
        return response({
          items: [message("invalid-state-message", "Prompt", "Answer", "private")],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const invalid = await runRenderer(invalidBridge);
  walk(
    invalid.document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button")
  )[0].click();
  await settle();
  assert.match(
    invalid.document.byId.get("status-card").textContent,
    /Malformed \/agent\/threads\/:uuid\/messages response/
  );
  assert.doesNotMatch(invalid.document.byId.get("message-list").textContent, /Answer/);
}

// A turn must carry the attachments the user staged. This regression is the
// reason worth having: the tray was cleared before startTurn read it, so
// fileIDs was always empty and every upload was silently dropped — the chips
// appeared, the upload succeeded, and the model never saw the file.
// The shim's external-link interception, which has no other home.
//
// It cannot be checked in a real webview: ExecJS queues until the Wails
// runtime reports itself loaded, and on a loopback origin it never does. So
// the shim is loaded here against a minimal DOM and a click is dispatched
// directly — which is also the better place for it, because this runs in CI.
//
// What must hold: the default action is prevented (otherwise the webview
// navigates and the app is replaced by a remote page, with no way back on a
// shell that has no cancellable navigation hook), and the URL is handed to Go
// rather than opened here.
// The task context panel must paint on load, not on the first interaction.
//
// It sits after the bootstrap call in renderer.js, so an initial render is not
// implicit — and a panel that never ran is indistinguishable from one that ran
// and found nothing, because index.html ships plausible-looking static values.

// Fences: the guard that decides whether a late answer may still be applied.
//
// There were eight of these as loose counters with the rules written in
// comments, and the rule that mattered most — a session change invalidates
// everything in flight — was enforced by every session-changing path
// remembering to bump four other counters by hand. It is a cascade now, and
// this is where the cascade is pinned: the property is not "session.bump()
// increments something", it is "a token taken from ANY of the covered fences
// before a session change is refused afterwards".
async function testFencesInvalidateOnBumpAndCascadeFromSession() {
  const { ns } = await runRenderer(undefined);

  // The primitive first, on a fence nobody else can touch.
  {
    const fence = ns.createFence("probe");
    const token = fence.snapshot();
    assert.equal(fence.isCurrent(token), true, "a fresh snapshot is current");
    fence.bump();
    assert.equal(fence.isCurrent(token), false, "a bump invalidates what was outstanding");
    const renewed = fence.snapshot();
    assert.equal(fence.isCurrent(renewed), true);
    assert.notEqual(renewed, token, "snapshots are monotonic, not reused");
    // Bumping twice must not wrap back onto a live token — the failure mode of
    // a boolean "is stale" flag, which this is deliberately not.
    fence.bump();
    assert.equal(fence.isCurrent(renewed), false);
  }

  // Dependents cascade; the parent is not affected by a child.
  {
    const child = ns.createFence("child");
    const parent = ns.createFence("parent", [child]);
    const parentToken = parent.snapshot();
    const childToken = child.snapshot();
    child.bump();
    assert.equal(child.isCurrent(childToken), false, "a child bump invalidates the child");
    assert.equal(parent.isCurrent(parentToken), true, "a child bump must NOT invalidate the parent");
    parent.bump();
    assert.equal(child.isCurrent(child.snapshot()), true);
    assert.equal(parent.isCurrent(parentToken), false);
  }

  // And now the real ones the renderer runs on. Every fence a session change
  // is documented to cover is listed, so adding a fence to the cascade without
  // adding it here — or removing one from the cascade — fails.
  {
    const covered = ["selection", "turn", "create", "recovery", "loginOperation"];
    const tokens = new Map(covered.map((name) => [name, ns.fences[name].snapshot()]));
    const sessionToken = ns.fences.session.snapshot();

    ns.fences.session.bump();

    assert.equal(
      ns.fences.session.isCurrent(sessionToken),
      false,
      "the session fence invalidates its own outstanding work",
    );
    for (const name of covered) {
      assert.equal(
        ns.fences[name].isCurrent(tokens.get(name)),
        false,
        `a session change must invalidate in-flight ${name} work`,
      );
    }
    // contentSearch is deliberately NOT covered: a search is scoped to the
    // thread list, which a session change rebuilds through its own path, and
    // widening the cascade without a reason to would make this test say
    // nothing about what the cascade is for.
    assert.equal(
      Object.keys(ns.fences).sort().join(","),
      ["session", ...covered, "contentSearch"].sort().join(","),
      "the fence roster changed; decide deliberately whether the new one is session-covered",
    );
  }
}

// Conversation grouping and search.
//
// Grouping is by local calendar day, not elapsed hours — the case that makes
// the difference is a conversation from late last night read at 1am: "3 hours
// ago" is true and useless, "Yesterday" is what someone is looking for. The
// two other rules are equally deliberate: a timestamp that will not parse goes
// to Older rather than vanishing, and a future one stays in Today.
async function testThreadGroupingAndSearch() {
  // Built from LOCAL date components, not UTC strings. The grouping is by
  // local calendar day, so a fixture written in UTC would pass or fail
  // depending on the machine's timezone — which is how the first version of
  // this test failed: "23:30 UTC yesterday" is this morning in UTC+8, and the
  // code was right to call it today.
  const local = (y, m, d, h = 9) => new Date(y, m - 1, d, h, 0, 0).toISOString();
  const now = new Date(2026, 7, 8, 10, 0, 0);
  const threads = [
    { uuid: "t-today", name: "Quarterly sales", updatedAt: local(2026, 8, 8, 1) },
    { uuid: "t-lastnight", name: "Late night", updatedAt: local(2026, 8, 7, 23) },
    { uuid: "t-week", name: "Pipeline notes", updatedAt: local(2026, 8, 3) },
    { uuid: "t-old", name: "Archived plan", updatedAt: local(2026, 6, 1) },
    { uuid: "t-broken", name: "Broken stamp", updatedAt: "not-a-date" },
    { uuid: "t-future", name: "Clock skew", updatedAt: local(2026, 8, 9) },
  ];

  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-08T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        // Built through the shared helper so the fixture satisfies the same
        // strict parser the renderer applies; only the timestamp is varied.
        // The malformed row is deliberately NOT served here: parseThread
        // rejects an unparseable updated_at and parseThreadList maps over
        // every item, so one bad row empties the entire sidebar. The grouping
        // function's own handling of it is asserted directly below instead.
        return response({
          items: threads
            .filter((t) => t.uuid !== "t-broken")
            .map((t) => ({ ...thread(t.uuid, t.name), updated_at: t.updatedAt })),
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn() { return { turnID: "unused" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);

  const groups = ns.groupThreads(
    threads.map((t) => ({ ...t, updated_at: t.updatedAt })),
    now,
  );
  // Array.from because these crossed a VM realm boundary: same contents,
  // different Array prototype, and deepStrictEqual cares.
  const ids = (bucket) => Array.from(bucket).map((t) => t.uuid).sort();
  assert.deepEqual(
    ids(groups.today),
    ["t-future", "t-today"],
    "today holds the same calendar day, and a future timestamp stays there rather than forming its own bucket",
  );
  assert.deepEqual(
    ids(groups.week),
    ["t-lastnight", "t-week"],
    "late last night belongs to the previous week bucket by calendar day, not to today by elapsed hours",
  );
  assert.deepEqual(
    ids(groups.older),
    ["t-broken", "t-old"],
    "an unparseable timestamp is kept in Older; dropping it would hide a real conversation",
  );

  // Group headings appear only for buckets that have something in them.
  const headings = Array.from(document.byId.get("thread-list").children).filter(
    (node) => node.classList?.contains("thread-group"),
  );
  assert.ok(headings.length >= 2, "populated groups must be labelled");

  // Searching happens in the palette now, not in the rail. Driven through the
  // palette's input rather than by poking state: that is the path a user takes,
  // and top-level consts in a VM script are not reachable from the context
  // object anyway.
  const listed = () =>
    Array.from(document.byId.get("thread-list").children)
      .filter((n) => n.classList?.contains("thread-item")).length;
  const before = listed();
  assert.ok(before >= 5, "precondition: the rail lists every cached conversation");

  document.dispatchKey({ key: "k", metaKey: true });
  const search = document.byId.get("quick-switcher-input");
  search.value = "quarterly";
  search.dispatch("input");
  const shown = walk(
    document.byId.get("quick-switcher-list"),
    (n) => n.classList?.contains("quick-switcher-item"),
  );
  assert.equal(shown.length, 1, "a query must narrow the palette to matching names");
  assert.equal(
    listed(),
    before,
    "and must NOT shorten the rail: the list behind a search window is not a result set",
  );

  search.value = "nothing-matches-this";
  search.dispatch("input");
  assert.match(
    document.byId.get("quick-switcher-list").textContent,
    /Nothing matches/,
    "an empty result must say so where the query was typed",
  );
}

// There is exactly one search in this app. The rail used to carry a second one
// — a field under the product name that filtered titles in place and asked the
// sidecar for message bodies — while ⌘K filtered the same titles and offered
// actions. Two controls called search, overlapping vocabularies, neither
// complete. Pinned here because the cheapest way to "fix" a palette that is
// missing something is to put a small search back in the rail.
{
  assert.doesNotMatch(rendererHTML, /id=["']thread-search["']/u);
  assert.doesNotMatch(rendererHTML, /id=["']thread-search-panel["']/u);
  assert.doesNotMatch(rendererHTML, /id=["']content-match-panel["']/u);
  assert.doesNotMatch(rendererSource, /threadSearchInput|threadQuery/u);
  assert.doesNotMatch(rendererCSS, /\.thread-search|\.content-match/u);
  // The rail's remaining entry point is an icon, and it opens the one palette.
  assert.match(rendererHTML, /id=["']sidebar-search-button["']/u);
  assert.match(rendererSource, /sidebarSearchButton/u);
}

// Icon-only controls. An icon is not a name: every one of these has to say
// what it does to a screen reader and to a hovering pointer, and the icon
// itself has to be hand-drawn inline SVG — the CSP allows no third-party
// script, and the bundled-renderer allowlist would have to grow a file for a
// sprite sheet.
{
  for (const [id, label] of [
    ["sidebar-search-button", "Search conversations and actions"],
    ["sidebar-collapse-button", "Hide sidebar"],
    ["context-panel-button", "Hide workspace panel"],
  ]) {
    const tag = rendererHTML.match(new RegExp(`<button\\s+id="${id}"[\\s\\S]*?>`, "u"));
    assert.ok(tag, `${id} must ship as a button`);
    assert.match(tag[0], new RegExp(`aria-label="${label}"`, "u"), `${id} needs a name`);
    assert.match(tag[0], /title="/u, `${id} needs a tooltip too — a name only a screen reader hears is half a label`);
    assert.match(
      tag[0],
      /aria-(?:expanded|haspopup)="/u,
      `${id} must say what pressing it does to the screen — open a dialog, or show and hide a column`,
    );
  }
  // Six inline icons, one drawing convention: 16px box, currentColor, no fill.
  assert.ok(
    rendererHTML.match(/<svg viewBox="0 0 16 16" aria-hidden="true" focusable="false">/gu).length >= 3,
    "the icons are inline SVG in the markup, drawn to one convention",
  );
  assert.match(rendererCSS, /\.icon-button svg \{[^}]*?stroke: currentColor;/u);
  assert.doesNotMatch(rendererHTML, /<script[^>]+src="http/u);
}

// With nothing cached the rail says so, and says nothing else: no filter above
// an empty list, which reads as "your search found nothing" when the truth is
// that nothing has been created yet.
async function testEmptyRailSaysNothingIsCached() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-08T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async listSkills() { return typedSuccess(pptCatalog()); },
    },
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  assert.match(
    document.byId.get("thread-list").textContent,
    /No cached threads yet/,
    "an empty rail must name its own emptiness",
  );
}

// With nothing in it, the rail is one sentence. Every section is absent —
// not present-and-empty — and the run line is down, because there is no run.
async function testTaskContextPanelRendersOnLoad() {
  const { document, ns } = await runRenderer(undefined);
  for (const id of ["context-deliverables", "context-sources", "context-retrieved"]) {
    assert.equal(
      document.byId.get(id).hidden,
      true,
      `${id} must stand down rather than explain its own emptiness`,
    );
  }
  assert.equal(
    document.byId.get("context-empty-note").hidden,
    false,
    "the whole empty state is one line",
  );
  assert.equal(
    document.byId.get("context-run-line").hidden,
    true,
    "no run, no live line",
  );
  assert.equal(
    document.byId.get("open-workspace-button").hidden,
    true,
    "nothing to open, no button",
  );
  // The panel is rendered by JS on load, not left at its markup default: a
  // static rail would leave these counts as the empty strings the markup ships.
  assert.equal(document.byId.get("sources-meta").textContent, "0 files");
  assert.equal(document.byId.get("deliverables-meta").textContent, "0 files");
}

async function testShimInterceptsExternalLinks() {
  const shimPath = path.join(rendererDir, "shim.js");
  const shimSource = fs.readFileSync(shimPath, "utf8");

  const listeners = [];
  const fetches = [];
  let prevented = false;

  const anchor = {
    tagName: "A",
    attrs: new Map(),
    getAttribute(name) { return this.attrs.get(name) ?? null; },
    closest(sel) { return sel === "a[href]" && this.attrs.has("href") ? this : null; },
  };

  const context = {
    console,
    URL,
    Headers,
    TextEncoder,
    crypto: { randomUUID: () => "00000000-0000-4000-8000-000000000001" },
    fetch: async (url, init) => {
      fetches.push({ url: String(url), init });
      return { ok: true, status: 200, statusText: "OK", url: String(url), headers: new Map(),
               text: async () => "{}", body: null };
    },
    setTimeout, clearTimeout,
    location: { origin: "http://127.0.0.1:5000" },
    document: {
      baseURI: "http://127.0.0.1:5000/CAPABILITY/",
      documentElement: { dataset: {} },
      addEventListener(type, handler, capture) { listeners.push({ type, handler, capture }); },
    },
  };
  context.window = context;
  // The shim stands down if a bridge is already installed, and needs the
  // generated library present; a stub is enough for the click path.
  context.window.__workmaxDesktopBridge = { createDesktopBridge: () => ({}) };
  vm.createContext(context);
  vm.runInContext(shimSource, context, { filename: shimPath });

  const click = listeners.find((l) => l.type === "click");
  assert.ok(click, "shim.js must register a click listener");
  assert.equal(click.capture, true,
    "the listener must be in the capture phase, or a handler that stops propagation skips it");

  // An external link: prevented and handed to Go.
  anchor.attrs.set("href", "https://github.com/jonnyquan/workmax");
  click.handler({ target: anchor, preventDefault() { prevented = true; } });
  await settle();
  assert.equal(prevented, true, "an external link must not be allowed to navigate the window");
  assert.equal(fetches.length, 1, "the URL must be handed to Go exactly once");
  assert.match(fetches[0].url, /\/CAPABILITY\/open-external$/,
    "posted to the capability-scoped open-external endpoint");
  assert.equal(JSON.parse(fetches[0].init.body).url, "https://github.com/jonnyquan/workmax");

  // A same-origin link is left alone: intercepting it would break in-app
  // navigation for no benefit.
  fetches.length = 0;
  prevented = false;
  anchor.attrs.set("href", "./index.html");
  click.handler({ target: anchor, preventDefault() { prevented = true; } });
  await settle();
  assert.equal(prevented, false, "a same-origin link must be left to the page");
  assert.equal(fetches.length, 0, "a same-origin link must not be sent to the system browser");

  // An in-page anchor likewise.
  anchor.attrs.set("href", "#section");
  click.handler({ target: anchor, preventDefault() { prevented = true; } });
  await settle();
  assert.equal(prevented, false, "a fragment link must be left to the page");
}

// A local-first product must be able to destroy its own data. This drives the
// renderer's half of thread deletion: the affordance appears only on
// local-only threads, arms on the first click, deletes on the second, and the
// deleted conversation leaves the screen without a refresh.
async function testThreadDeleteIsTwoStepAndLocalOnly() {
  const deleted = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          items: [
            { ...thread("00000000-0000-4000-8000-00000000d001", "Local scratch"), cloud_sync_state: "local" },
            { ...thread("00000000-0000-4000-8000-00000000d002", "Synced deck"), cloud_sync_state: "synced" },
          ],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      async deleteThread(uuid) {
        deleted.push(uuid);
        return typedSuccess({ deleted: true, messages: 2, files: 0, turn_intents: 1, index_cleanups: 0 });
      },
      startTurn() { return { turnID: "t" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  const deleteButtons = () =>
    walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-delete"));

  assert.equal(
    deleteButtons().length,
    1,
    "delete must be offered on the local thread and only there — a synced thread's delete would undo itself",
  );

  // Select the thread first so the test also proves deletion clears the
  // selection rather than leaving a workbench pointed at nothing.
  walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-button"))[0].click();
  await settle();

  // The two clicks and the between-click assertion run in one tick: the VM
  // maps setTimeout to setImmediate, so the 4-second disarm fires as soon as
  // the test yields. Synchronous is also the honest reading of the contract —
  // arming is instantaneous, only DISarming waits.
  const del = deleteButtons()[0];
  del.click();
  assert.equal(deleted.length, 0, "the first click must arm, not delete");
  assert.equal(del.textContent, "Confirm", "the armed control must say what the next click does");
  del.click();
  await settle();
  assert.deepEqual(Array.from(deleted), ["00000000-0000-4000-8000-00000000d001"]);
  assert.equal(
    walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-delete")).length,
    0,
    "the deleted thread must leave the list (and the synced one never had a button)",
  );
  assert.doesNotMatch(document.byId.get("thread-list").textContent, /Local scratch/);
  assert.match(document.byId.get("thread-list").textContent, /Synced deck/);
  assert.equal(
    document.byId.get("thread-panel").hidden,
    true,
    "deleting the selected thread must close its workbench",
  );
}

// Files arrive however the user's hands bring them — picker, drop, paste —
// and all three must land in the same pipeline: chips, Sources, send union.
async function testDropAndPasteAttachFiles() {
  const uploaded = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("drop-thread", "Droppable")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile(uuid, file) {
        uploaded.push(file.name);
        return typedSuccess({ file_id: 100 + uploaded.length, file_name: file.name, mime_type: "text/plain", file_type: "text", file_size: 10 });
      },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn() { return { turnID: "t" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-button"))[0].click();
  await settle();

  const panel = document.byId.get("thread-panel");
  panel.dispatch("dragover", { dataTransfer: { files: [] } });
  assert.equal(panel.classList.contains("drop-target"), true, "hovering files must light the target");
  panel.dispatch("drop", { dataTransfer: { files: [{ name: "dropped.txt", size: 10 }] } });
  await settle();
  assert.equal(panel.classList.contains("drop-target"), false, "the drop retires the highlight");
  assert.deepEqual(Array.from(uploaded), ["dropped.txt"], "a dropped file rides the same upload path");

  document.byId.get("chat-input").dispatch("paste", {
    clipboardData: { files: [{ name: "pasted.png", size: 22 }] },
  });
  await settle();
  assert.deepEqual(Array.from(uploaded), ["dropped.txt", "pasted.png"], "a pasted file too");
}

// Picking several files at once must land several attachments. The old fence
// was a single shared counter: each new upload invalidated every one already
// in flight, so of a multi-select only the last file ever became "ready" and
// the rest froze on "uploading". Each upload now completes independently.
async function testMultiFileUploadCompletesEveryFile() {
  const resolvers = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("multi-thread", "Multi")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const sentFileIDs = [];
  const desktopBridge = {
    agent: {
      uploadThreadFile(uuid, file) {
        // Held open until the test releases them: both uploads must be in
        // flight at once, or the shared-counter regression cannot show.
        return new Promise((resolve) => {
          resolvers.push({ name: file.name, resolve });
        });
      },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        sentFileIDs.push(Array.from(input.fileIDs ?? []));
        callback({ type: "text_delta", turnID: "multi-turn", delta: "ok" });
        callback({ type: "done", turnID: "multi-turn", result: { code: "", subtype: "", is_error: false } });
        return { turnID: "multi-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-button"))[0].click();
  await settle();

  const input = document.byId.get("file-input");
  input.files = [
    { name: "first.txt", size: 10 },
    { name: "second.txt", size: 20 },
  ];
  input.dispatch("change");
  await settle();

  const chips = () =>
    walk(document.byId.get("attachment-chips"), (n) => n.classList?.contains("attachment-chip"));
  assert.equal(chips().length, 2, "both picked files must get a chip");
  assert.match(chips()[0].textContent, /first\.txt…/, "in flight: uploading");
  assert.match(chips()[1].textContent, /second\.txt…/, "in flight: uploading");

  // Resolve out of order: the first pick finishing last is exactly the shape
  // the shared counter used to drop.
  assert.equal(resolvers.length, 2, "both uploads must actually start");
  resolvers[1].resolve(typedSuccess({ file_id: 202 }));
  await settle();
  resolvers[0].resolve(typedSuccess({ file_id: 201 }));
  await settle();

  assert.equal(chips().length, 2);
  assert.equal(chips()[0].textContent, "first.txt", "every upload reaches ready, not only the last");
  assert.equal(chips()[1].textContent, "second.txt", "every upload reaches ready, not only the last");

  // "ready" must be real: both ids ride the next turn.
  const chat = document.byId.get("chat-input");
  chat.value = "Use both files";
  chat.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();
  assert.deepEqual(
    Array.from(sentFileIDs[0]).sort(),
    [201, 202],
    "both completed uploads must reach startTurn",
  );
}

// A rejected upload used to become an unhandled promise rejection and a chip
// stuck on "uploading" forever. It must land in the failed state instead, and
// the chip must offer a way back: retry the same file, or remove it.
async function testRejectedUploadFailsTheChipWithoutUnhandledRejection() {
  let uploadAttempts = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("fail-thread", "Failing")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        uploadAttempts += 1;
        if (uploadAttempts === 1) throw new Error("bridge transport failed");
        return typedSuccess({ file_id: 301 });
      },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn() { return { turnID: "t" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const unhandled = [];
  const trap = (reason) => unhandled.push(reason);
  process.on("unhandledRejection", trap);
  try {
    const { document, ns } = await runRenderer(bridge, desktopBridge);
    walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-button"))[0].click();
    await settle();

    const input = document.byId.get("file-input");
    input.files = [{ name: "doomed.txt", size: 5 }];
    input.dispatch("change");
    await settle();
    await settle();

    const chips = () =>
      walk(document.byId.get("attachment-chips"), (n) => n.classList?.contains("attachment-chip"));
    assert.equal(chips().length, 1, "the failed file keeps its chip");
    assert.equal(chips()[0].classList.contains("error"), true, "the chip must land in the failed state");
    assert.match(chips()[0].textContent, /doomed\.txt ✗/, "the failure is visible, not a forever-spinner");
    assert.deepEqual(unhandled, [], "a failed upload must not leak an unhandled rejection");

    // Retry runs the same file again and can succeed.
    const retry = walk(chips()[0], (n) => n.classList?.contains("attachment-chip-retry"))[0];
    assert.ok(retry, "a failed chip offers retry");
    retry.click();
    await settle();
    assert.equal(uploadAttempts, 2, "retry re-runs the upload");
    assert.equal(chips()[0].textContent, "doomed.txt", "a successful retry reaches ready");

    // And a failed chip can simply be taken out of the tray.
    desktopBridge.agent.uploadThreadFile = async () => {
      uploadAttempts += 1;
      throw new Error("still failing");
    };
    input.files = [{ name: "unwanted.txt", size: 5 }];
    input.dispatch("change");
    await settle();
    await settle();
    assert.equal(chips().length, 2);
    const remove = walk(chips()[1], (n) => n.classList?.contains("attachment-chip-remove"))[0];
    assert.ok(remove, "a failed chip offers removal");
    remove.click();
    await settle();
    assert.equal(chips().length, 1, "remove takes the failed chip out of the tray");
    assert.deepEqual(unhandled, [], "no unhandled rejection across fail, retry, and remove");
  } finally {
    process.removeListener("unhandledRejection", trap);
  }
}

// A half-written prompt must survive a thread switch: each thread keeps its
// own draft, restored on the way back. Until this existed the composer was
// one shared box — the outgoing thread's words leaked into the next thread
// and were lost the moment anything was typed there.
async function testComposerDraftSurvivesThreadSwitch() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          items: [thread("draft-a", "Draft A"), thread("draft-b", "Draft B")],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn() { return { turnID: "t" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  const threadButtons = () =>
    walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-button"));
  const input = document.byId.get("chat-input");

  threadButtons()[0].click();
  await settle();
  input.value = "half-written long prompt for A";
  input.dispatch("input");

  threadButtons()[1].click();
  await settle();
  assert.equal(input.value, "", "thread B starts with its own (empty) composer, not A's words");
  input.value = "notes for B";
  input.dispatch("input");

  threadButtons()[0].click();
  await settle();
  assert.equal(
    input.value,
    "half-written long prompt for A",
    "switching back must restore thread A's draft",
  );

  threadButtons()[1].click();
  await settle();
  assert.equal(input.value, "notes for B", "thread B's draft survives too");
}

// Regenerate re-runs the FINAL prompt — the click is the consent, and only
// the last answer is re-runnable: forking an earlier exchange would create a
// history the transcript cannot show.
async function testRegenerateRunsTheLastPromptAgain() {
  const started = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("regen-thread", "Regen")] });
      }
      if (pathname === "/agent/threads/regen-thread/messages") {
        return response({
          items: [
            message("r1", "first question", "first answer"),
            message("r2", "second question", "second answer"),
          ],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input) {
        started.push(input.userText);
        return { turnID: "regen-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-button"))[0].click();
  await settle();

  const regens = walk(
    document.byId.get("message-list"),
    (n) => n.tagName === "BUTTON" && n.textContent === "Regenerate",
  );
  assert.equal(regens.length, 1, "only the final answer offers Regenerate");
  regens[0].click();
  await settle();
  assert.deepEqual(Array.from(started), ["second question"], "the same words run again as a new turn");
}

// ⌘K: reach any conversation without leaving the keyboard. Filter, arrow,
// Enter — and Escape closes the palette before it ever touches a turn.
async function testQuickSwitcherJumpsBetweenThreads() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          items: [
            thread("00000000-0000-4000-8000-0000000ck001", "Quarterly deck"),
            thread("00000000-0000-4000-8000-0000000ck002", "Launch notes"),
            thread("00000000-0000-4000-8000-0000000ck003", "Quarterly review"),
          ],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn() { return { turnID: "t" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  assert.equal(document.byId.get("quick-switcher").hidden, true, "closed at rest");

  document.dispatchKey({ key: "k", metaKey: true });
  assert.equal(document.byId.get("quick-switcher").hidden, false, "⌘K opens the palette");
  assert.equal(
    walk(document.byId.get("quick-switcher-list"), (n) => n.classList?.contains("quick-switcher-item")).length,
    3,
    "all conversations offered before any query",
  );

  const input = document.byId.get("quick-switcher-input");
  input.value = "quarterly";
  input.dispatch("input");
  let items = walk(document.byId.get("quick-switcher-list"), (n) => n.classList?.contains("quick-switcher-item"));
  assert.equal(items.length, 2, "the query narrows the list");
  assert.equal(items[0].classList.contains("active"), true, "the first candidate starts active");

  input.dispatch("keydown", { key: "ArrowDown" });
  items = walk(document.byId.get("quick-switcher-list"), (n) => n.classList?.contains("quick-switcher-item"));
  assert.equal(items[1].classList.contains("active"), true, "arrows move the selection");

  input.dispatch("keydown", { key: "Enter" });
  await settle();
  assert.equal(document.byId.get("quick-switcher").hidden, true, "choosing closes the palette");
  assert.match(
    document.byId.get("thread-title").textContent,
    /Quarterly review/,
    "Enter lands in the highlighted conversation",
  );

  // Escape closes the palette and must NOT be treated as stop-the-turn.
  document.dispatchKey({ key: "k", metaKey: true });
  assert.equal(document.byId.get("quick-switcher").hidden, false);
  document.dispatchKey({ key: "Escape" });
  assert.equal(document.byId.get("quick-switcher").hidden, true, "Escape closes the palette first");
}

// Escape is the keyboard's Stop button — same act, no mouse.
async function testEscapeStopsAStreamingTurn() {
  const cancels = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("esc-thread", "Escapable")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn() { return { turnID: "esc-turn" }; },
      async cancelTurn(turnID) {
        cancels.push(turnID);
        return { turnID, canceled: true };
      },
    },
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  const input = document.byId.get("chat-input");
  input.value = "long task";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();
  assert.equal(document.byId.get("stop-button").hidden, false, "precondition: streaming");

  // With the palette open, Escape means "close the palette" — the streaming
  // turn must survive that keystroke untouched.
  document.dispatchKey({ key: "k", metaKey: true });
  assert.equal(document.byId.get("quick-switcher").hidden, false);
  document.dispatchKey({ key: "Escape" });
  await settle();
  assert.equal(document.byId.get("quick-switcher").hidden, true);
  assert.equal(cancels.length, 0, "closing the palette must not stop the turn");

  // The mind panel is the other surface Escape has to share the key with, and
  // it shares it differently: it is a COLUMN, not an overlay, so it is meant to
  // stay open beside a running turn for as long as the reader wants it. That
  // makes "Escape closes the mind" wrong as a global rule — it would disarm the
  // stop key for the entire window the moment the column was left open.
  const root = document.documentElement;
  const brain = document.byId.get("mind-button");
  brain.click();
  await settle();
  assert.equal(root.getAttribute("data-right-panel"), "mind", "precondition: the panel is up");

  // From inside the panel, Escape dismisses the panel and goes no further —
  // the turn must not be stopped on the way out.
  brain.focused = false;
  document.byId.get("mind-panel").dispatch("keydown", { key: "Escape" });
  await settle();
  assert.equal(
    root.getAttribute("data-right-panel"),
    null,
    "Escape inside the panel is the same act as pressing the icon: the column goes back",
  );
  assert.equal(brain.focused, true, "and hands focus back to the icon it came from");
  assert.equal(cancels.length, 0, "dismissing the panel must not stop the turn");

  // With the panel open, Escape anywhere else is still the stop key, and the
  // column the reader chose to keep stays where it is.
  brain.click();
  await settle();
  document.dispatchKey({ key: "Escape" });
  await settle();
  assert.equal(
    root.getAttribute("data-right-panel"),
    "mind",
    "Escape elsewhere must not fold a column the reader chose to keep",
  );
  assert.deepEqual(Array.from(cancels), ["esc-turn"], "Escape must stop the turn");
}

// The capacity note appears only when it matters, and speaks in percent —
// the limit is bytes, and "characters left" would lie to CJK text by 3x.
async function testComposerCapacityNote() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("cap-thread", "Capacity")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn() { return { turnID: "t" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  const input = document.byId.get("chat-input");
  const note = document.byId.get("composer-capacity");
  input.value = "short";
  input.dispatch("input");
  assert.equal(note.hidden, true, "quiet until it matters");

  input.value = "x".repeat(60000); // ~92% of the 65536-byte limit
  input.dispatch("input");
  assert.equal(note.hidden, false);
  assert.match(note.textContent, /9\d% of the message limit/, "percent, not a character count");

  input.value = "back to short";
  input.dispatch("input");
  assert.equal(note.hidden, true, "and quiet again once there is room");
}

// A reader who scrolled up to check an earlier answer must not be yanked
// back down by every streaming delta — the stream follows only a reader who
// is already following, and otherwise lights the jump affordance.
async function testStreamingDoesNotYankAScrolledUpReader() {
  let emit = null;
  const manyMessages = Array.from({ length: 12 }, (_, i) => ({
    uuid: `m${i}`,
    user_text: `question ${i}`,
    ai_text: `answer ${i}`,
    streaming_state: "complete",
    created_at: "2026-05-21T00:00:00Z",
    updated_at: "2026-05-21T00:00:00Z",
  }));
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("scroll-thread", "Long chat")] });
      }
      if (pathname === "/agent/threads/scroll-thread/messages") {
        return response({ items: manyMessages });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "scroll-turn" });
        return { turnID: "scroll-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  // The stub's elements are not a connected tree, so the viewport's derived
  // scrollHeight is useless; pin explicit geometry on this one instance so
  // the sticky math has something real to chew on.
  const viewport = document.byId.get("message-viewport");
  Object.defineProperty(viewport, "scrollHeight", { value: 1200 });
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  assert.equal(viewport.scrollTop, 1200, "opening a thread lands at the newest message");

  const input = document.byId.get("chat-input");
  input.value = "One more question";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  // The reader scrolls up to re-read something.
  viewport.scrollTop = 0;
  viewport.dispatch("scroll");

  const before = viewport.scrollTop;
  emit({ type: "text_delta", delta: "streaming while they read " });
  emit({ type: "text_delta", delta: "and more " });
  await settle();
  assert.equal(viewport.scrollTop, before, "deltas must not scroll a reader who scrolled away");
  assert.equal(
    document.byId.get("jump-latest").hidden,
    false,
    "the way back down must be offered instead",
  );

  document.byId.get("jump-latest").click();
  await settle();
  assert.ok(viewport.scrollTop >= viewport.scrollHeight - 500, "jump returns to the newest");
  assert.equal(document.byId.get("jump-latest").hidden, true, "and retires itself");

  // Scroll up once more, then return by HAND (not via the button, whose
  // force path sets stickiness itself): the scroll listener alone must
  // notice the reader is back and retire the jump affordance.
  viewport.scrollTop = 0;
  viewport.dispatch("scroll");
  emit({ type: "text_delta", delta: "light the button again " });
  await settle();
  assert.equal(document.byId.get("jump-latest").hidden, false, "precondition: away again");
  viewport.scrollTop = 1200;
  viewport.dispatch("scroll");
  assert.equal(
    document.byId.get("jump-latest").hidden,
    true,
    "scrolling back down by hand must resume following and retire the button",
  );
  emit({ type: "text_delta", delta: "now following again" });
  await settle();
  assert.equal(viewport.scrollTop, 1200, "a following reader keeps following");

  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();
}

// The performance gate, stated as operation counts the stub can see. A
// correctness assertion cannot tell one full-text repaint from five hundred;
// these counters can. Per delta the renderer may do bookkeeping only — the
// DOM work must scale with flushed frames, not with tokens.
async function testStreamingDeltaCostsStayConstant() {
  let emit = null;
  const tokens = Array.from({ length: 500 }, (_, i) => `tok${i} `);
  const answer = tokens.join("");
  let answered = false;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("perf-thread", "Fire hose")] });
      }
      if (pathname === "/agent/threads/perf-thread/messages") {
        return response({
          items: answered
            ? [{
                uuid: "perf-msg", user_text: "Count tokens", ai_text: answer,
                streaming_state: "complete",
                created_at: "2026-05-21T00:00:00Z", updated_at: "2026-05-21T00:00:00Z",
              }]
            : [],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "perf-turn" });
        return { turnID: "perf-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Count tokens";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  const bubble = walk(
    document.byId.get("message-list"),
    (n) => n.classList?.contains("bubble") && n.parentNode?.classList?.contains("assistant"),
  ).at(-1);
  // Count full-content resets on the streaming bubble itself: the O(n²)
  // regression was one of these per token.
  const proto = Object.getPrototypeOf(bubble);
  const descriptor = Object.getOwnPropertyDescriptor(proto, "textContent");
  let bubbleContentResets = 0;
  Object.defineProperty(bubble, "textContent", {
    configurable: true,
    get() { return descriptor.get.call(this); },
    set(value) {
      bubbleContentResets += 1;
      descriptor.set.call(this, value);
    },
  });
  const querySelectorBase = document.querySelectorCalls;
  const appendDataBase = document.appendDataCalls;

  // 500 deltas in 5 settled bursts: within a burst nothing may touch the DOM;
  // each settle flushes exactly one frame.
  for (let burst = 0; burst < 5; burst += 1) {
    for (let i = 0; i < 100; i += 1) {
      emit({ type: "text_delta", delta: tokens[burst * 100 + i] });
    }
    await settle();
  }

  assert.equal(
    document.appendDataCalls - appendDataBase,
    5,
    "the merged text of each burst must land as one appendData per flushed frame",
  );
  assert.ok(
    bubbleContentResets <= 5,
    `streaming must append, never reset the bubble's content (saw ${bubbleContentResets} resets for 500 deltas)`,
  );
  assert.ok(
    document.querySelectorCalls - querySelectorBase <= 5,
    `selector lookups must not scale with the delta count (saw ${document.querySelectorCalls - querySelectorBase})`,
  );

  answered = true;
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();
  assert.equal(
    bubble.textContent,
    answer.trim(),
    "batching must not lose or reorder a single token",
  );
}

// The causal fence: buffered deltas land in the DOM before any non-delta
// event is reflected, so tool steps can never appear ahead of the words that
// preceded them — and a synchronous done paints everything, in order, without
// waiting for a frame.
async function testNonDeltaEventsDrainBufferedText() {
  let emit = null;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("order-thread", "Causal order")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "order-turn" });
        return { turnID: "order-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Interleave";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  const bubble = walk(
    document.byId.get("message-list"),
    (n) => n.classList?.contains("bubble") && n.parentNode?.classList?.contains("assistant"),
  ).at(-1);

  // The tool step must not outrun the buffered words before it.
  emit({ type: "text_delta", delta: "One " });
  emit({ type: "tool_use", name: "Write", target: "outline.md" });
  assert.equal(
    bubble.textContent,
    "One ",
    "a non-delta event must drain buffered text before it is handled",
  );

  emit({ type: "text_delta", delta: "two " });
  emit({ type: "tool_denied", name: "Write", target: "escape.txt", reason: "outside" });
  emit({ type: "text_delta", delta: "three." });
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  // Everything above happened in one synchronous burst; done drained it all.
  assert.equal(
    bubble.textContent,
    "One two three.",
    "a synchronous done must paint the complete text, in order, without waiting for a frame",
  );
  assert.equal(
    walk(document.byId.get("message-list"), (n) => n.classList?.contains("worklog-step")).length,
    2,
    "the interleaved tool steps must all be on the log",
  );
  await settle();
}

// The post-turn reconcile leaves the transcript's DOM alone when the snapshot
// confirms what was streamed: the completed pair is updated in place —
// timestamps, Regenerate, partial flag — and every other row keeps its very
// nodes. A snapshot that disagrees with the stream falls back to the full
// rebuild, whose correctness stays the reference.
async function testCompletedTurnReconcilesInPlace() {
  let emit = null;
  let turnSeq = 0;
  const prior = [
    message("r1", "first question", "first answer"),
    message("r2", "second question", "second answer"),
  ];
  let snapshot = prior;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("inplace-thread", "Stable rows")] });
      }
      if (pathname === "/agent/threads/inplace-thread/messages") {
        return response({ items: snapshot });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        turnSeq += 1;
        const turnID = `inplace-turn-${turnSeq}`;
        emit = (event) => callback({ ...event, turnID });
        return { turnID };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const list = document.byId.get("message-list");
  const beforeNodes = Array.from(list.children);
  assert.equal(beforeNodes.length, 4, "precondition: two cached exchanges, four rows");
  assert.equal(
    walk(list, (n) => n.classList?.contains("message-action-regenerate")).length,
    1,
    "precondition: the final cached answer offers Regenerate",
  );

  const input = document.byId.get("chat-input");
  input.value = "third question";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();
  const during = Array.from(list.children);
  assert.equal(during.length, 6, "the optimistic pair joins the transcript");
  const userNode = during[4];
  const assistantNode = during[5];
  assert.equal(
    walk(list, (n) => n.classList?.contains("message-action-regenerate")).length,
    0,
    "the superseded answer loses Regenerate the moment a new exchange starts",
  );

  emit({ type: "text_delta", delta: "third answer" });
  await settle();
  snapshot = [...prior, message("r3", "third question", "third answer")];
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();

  const after = Array.from(list.children);
  assert.equal(after.length, 6, "a confirming snapshot must not change the row count");
  for (let i = 0; i < 4; i += 1) {
    assert.equal(
      after[i],
      beforeNodes[i],
      `row ${i} must keep its node — the reconcile may not rebuild the transcript`,
    );
  }
  assert.equal(after[4], userNode, "the user row is updated in place, not replaced");
  assert.equal(after[5], assistantNode, "the assistant row is updated in place, not replaced");
  assert.equal(assistantNode.classList.contains("partial"), false);
  assert.equal(assistantNode.classList.contains("pending"), false);
  assert.equal(
    walk(userNode, (n) => n.classList?.contains("message-time")).length,
    1,
    "the reconcile stamps the stored time on the question",
  );
  assert.equal(
    walk(assistantNode, (n) => n.classList?.contains("message-time")).length,
    1,
    "and on the answer",
  );
  const regens = walk(list, (n) => n.classList?.contains("message-action-regenerate"));
  assert.equal(regens.length, 1, "exactly one Regenerate, on the new final answer");
  assert.equal(
    walk(assistantNode, (n) => n === regens[0]).length,
    1,
    "and it lives on the in-place assistant row",
  );

  // Fallback: a snapshot that disagrees with the stream (server-side rewrite)
  // must rebuild rather than trust the streamed DOM.
  input.value = "fourth question";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();
  emit({ type: "text_delta", delta: "draft answer" });
  await settle();
  snapshot = [...snapshot, message("r4", "fourth question", "server-rewritten answer")];
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();
  const rebuilt = Array.from(list.children);
  assert.equal(rebuilt.length, 8);
  assert.notEqual(
    rebuilt[0],
    beforeNodes[0],
    "a mismatched snapshot takes the full-rebuild path",
  );
  assert.match(list.textContent, /server-rewritten answer/);
  assert.doesNotMatch(
    list.textContent,
    /draft answer/,
    "the rebuild shows the server's text, not the stream's",
  );
}

// Streaming Markdown commits are only ever blocks the final parse would have
// produced: closed by a blank line outside any open fence. An unclosed fence
// stays raw in the tail however long it runs, and once the turn finishes the
// committed-plus-final DOM must be indistinguishable from a one-shot parse of
// the whole answer.
async function testStreamingMarkdownCommitsMatchTheFinalParse() {
  let emit = null;
  const part1 = "## Plan\n\nFirst paragraph of the answer.\n\n```js\nlet x = 1;\n";
  const part2 = "let y = 2;\n```\n\n- alpha\n- beta\n\nclosing tail line";
  const answer = part1 + part2;
  let answered = false;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("mdstream-thread", "Streamed markdown")] });
      }
      if (pathname === "/agent/threads/mdstream-thread/messages") {
        return response({
          items: answered
            ? [{
                uuid: "mdstream-msg", user_text: "Stream it", ai_text: answer,
                streaming_state: "complete",
                created_at: "2026-05-21T00:00:00Z", updated_at: "2026-05-21T00:00:00Z",
              }]
            : [],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "mdstream-turn" });
        return { turnID: "mdstream-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Stream it";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  emit({ type: "text_delta", delta: part1 });
  await settle();
  const bubble = walk(
    document.byId.get("message-list"),
    (n) => n.classList?.contains("bubble") && n.parentNode?.classList?.contains("assistant"),
  ).at(-1);
  assert.equal(bubble.classList.contains("markdown"), true, "closed blocks are typeset mid-stream");
  assert.equal(
    walk(bubble, (n) => n.tagName === "H5").length,
    1,
    "the finished heading is committed",
  );
  assert.equal(
    walk(bubble, (n) => n.tagName === "PRE").length,
    0,
    "an unclosed fence must NOT be committed — its shape is still ambiguous",
  );
  const tailDuring = walk(bubble, (n) => n.classList?.contains("md-stream-tail"))[0];
  assert.ok(tailDuring, "the open fence waits in the raw tail");
  assert.equal(tailDuring.textContent, "```js\nlet x = 1;\n");

  emit({ type: "text_delta", delta: part2 });
  await settle();
  const pre = walk(bubble, (n) => n.tagName === "PRE");
  assert.equal(pre.length, 1, "the closed fence commits on the next frame");
  assert.equal(pre[0].textContent, "let x = 1;\nlet y = 2;");
  // A block committed mid-stream is highlighted like any other: the idle pass
  // is hung off the block, not off the end of the turn, so a long answer is
  // coloured as it arrives rather than all at once at the finish line. Its text
  // is unchanged by that, which the assertion above already pinned.
  assert.ok(
    walk(pre[0], (n) => typeof n.className === "string" && n.className.startsWith("tok-")).length >
      0,
    "a code block committed while the turn is still streaming must be highlighted too",
  );
  assert.equal(walk(bubble, (n) => n.tagName === "LI").length, 2, "the closed list commits too");
  assert.equal(
    walk(bubble, (n) => n.classList?.contains("md-stream-tail"))[0].textContent,
    "closing tail line",
    "the still-open final line stays raw",
  );

  answered = true;
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();

  // The consistency pin: the same input through the one-shot parser must
  // produce the same text and the same element shape as the streamed commits
  // plus the finish-line tail parse.
  const reference = document.createElement("div");
  ns.renderMarkdownInto(reference, answer);
  // Syntax highlighting is deferred to idle time, so the reference has only
  // just scheduled its own. Let it land: the pin is that the two agree once
  // both have finished, and comparing a highlighted bubble against a reference
  // caught mid-flight would compare timing, not structure.
  await settle();
  const shape = (root) =>
    walk(root, (n) => n.tagName !== "#text")
      .map((n) => n.tagName)
      .join(">");
  assert.equal(
    bubble.textContent,
    reference.textContent,
    "streamed commits plus the final tail must read exactly like a one-shot parse",
  );
  assert.equal(
    shape(bubble).replace(/^DIV/u, ""),
    shape(reference).replace(/^DIV/u, ""),
    "and produce the same element structure",
  );
  assert.equal(
    walk(bubble, (n) => n.classList?.contains("md-stream-tail")).length,
    0,
    "the finished answer carries no streaming scaffolding",
  );
}

// The pill's colour is its state at a glance; the class must track the label.
async function testTurnStatePillCarriesItsTone() {
  let emit = null;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("tone-thread", "Tones")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "tone-turn" });
        return { turnID: "tone-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  const pill = document.byId.get("turn-state");
  const input = document.byId.get("chat-input");
  input.value = "go";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();
  assert.equal(pill.classList.contains("is-busy"), true, "Working must read busy");

  emit({ type: "text_delta", delta: "ok" });
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();
  assert.equal(pill.classList.contains("is-ok"), true, "Done must settle green");
  assert.equal(pill.classList.contains("is-busy"), false, "and stop pulsing");
}

// A failed turn must say so everywhere at once: red pill with its duration,
// and the run overview's execution step flipping to Failed. The duration is
// made deterministic by rewinding the turn's start stamp — real elapsed time
// in a test rounds to 0s and cannot catch a broken clock.
async function testFailedTurnStateAndDuration() {
  let emit = null;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("fail-thread", "Doomed run")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "fail-turn" });
        return { turnID: "fail-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };
  const { document, context, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  const input = document.byId.get("chat-input");
  input.value = "doomed";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  ns.state.activeTurn.startedAt -= 65000;
  emit({
    type: "proxy_error",
    error: { kind: "service_unavailable", message: "endpoint is down", retryable: true },
  });
  await settle();

  assert.match(
    document.byId.get("turn-state").textContent,
    /^Error · 1m \d+s$/,
    "the pill must carry the real elapsed time, minutes and all",
  );
  assert.equal(document.byId.get("turn-state").classList.contains("is-error"), true);
  // The outcome is the pill's job and only the pill's. The rail's live line
  // is a pointer at a running loop, so a settled turn puts it down rather
  // than restating "Failed" a second time in a second vocabulary.
  assert.equal(
    document.byId.get("context-run-line").hidden,
    true,
    "a turn that ended takes the live line with it",
  );
}

// The protocol picker answers its own question at the moment of choice.
async function testModelProtocolHintFollowsTheChoice() {
  const { document, ns } = await runRenderer(undefined);
  const protocol = document.byId.get("model-protocol");
  const hint = document.byId.get("model-protocol-hint");
  const baseURL = document.byId.get("model-base-url");

  protocol.value = "openai_compatible";
  protocol.dispatch("change");
  assert.match(hint.textContent, /Chat only/i, "openai must say what it does not get");
  assert.match(baseURL.placeholder, /11434/, "and hint at the Ollama-shaped URL");

  protocol.value = "anthropic_compatible";
  protocol.dispatch("change");
  assert.match(hint.textContent, /Enables the agent tool loop/i, "anthropic must say what it unlocks");
  assert.doesNotMatch(
    hint.textContent,
    /Chat only/i,
    "the hint must actually switch — both texts mention the tool loop, so the assertion must key on what only one of them says",
  );
}

// The L2 tool loop's renderer surface: activity narrated while it runs,
// denials visible, and the files it produced listed as deliverables.
async function testToolLoopActivityAndDeliverables() {
  let emit = null;
  let turnDone = false;
  const revealed = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("00000000-0000-4000-8000-0000000d2c01", "Tool loop")] });
      }
      if (pathname === "/agent/threads/00000000-0000-4000-8000-0000000d2c01/messages") {
        return response({
          items: turnDone
            ? [{
                uuid: "l2c-msg", user_text: "Build the deck", ai_text: "Deck ready.",
                streaming_state: "complete",
                created_at: "2026-08-09T10:00:00Z", updated_at: "2026-08-09T10:00:00Z",
              }]
            : [],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      async revealWorkspace(uuid) {
        revealed.push(uuid);
        return typedSuccess({ revealed: true });
      },
      async listWorkspaceFiles() {
        // One file predates the turn (same path, same mtime in both
        // listings): it must be in the panel but NOT among what this turn
        // "produced" — the produced rows are a diff, not the inventory.
        const old = { path: "old-draft.txt", size: 100, modified_at: "2026-08-01T00:00:00Z" };
        return typedSuccess({
          items: turnDone
            ? [
                { path: "deck/outline.md", size: 2048, modified_at: "2026-08-09T10:00:00Z" },
                { path: "notes.txt", size: 512, modified_at: "2026-08-09T09:59:00Z" },
                old,
              ]
            : [old],
          count: turnDone ? 3 : 1,
          truncated: false,
        });
      },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "l2-turn" });
        return { turnID: "l2-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  assert.equal(
    document.byId.get("deliverables-meta").textContent,
    "1 file",
    "the pre-existing file is inventory",
  );

  const input = document.byId.get("chat-input");
  input.value = "Build the deck";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  emit({ type: "tool_use", name: "Write", target: "outline.md" });
  await settle();
  assert.match(
    document.byId.get("context-run-line").textContent,
    /Step 1.*Write · outline\.md/su,
    "while a turn runs the rail points at what the loop is on",
  );

  // The step also lands inline: the transcript is a work log, not a chat
  // with a hidden engine room.
  {
    const steps = walk(document.byId.get("message-list"), (n) => n.classList?.contains("worklog-step"));
    assert.equal(steps.length, 1, "the running tool must appear as an inline step");
    assert.match(steps[0].textContent, /Write.*outline\.md/su, "the step names its target");
  }

  emit({ type: "tool_denied", name: "Write", target: "escape.txt", reason: "outside the workspace" });
  await settle();
  {
    const denied = walk(document.byId.get("message-list"), (n) => n.classList?.contains("denied"));
    assert.equal(denied.length, 1, "the denial must appear inline too");
    assert.match(denied[0].textContent, /blocked — outside the workspace/su);
  }

  emit({ type: "tool_use", name: "Edit", target: "outline.md" });
  emit({ type: "tool_use", name: "Read", target: "notes.txt" });
  emit({ type: "tool_use", name: "Grep", target: "" });
  // Five steps now — live, every one must be on screen: watching the agent
  // work is the point of a streaming log.
  assert.equal(
    walk(document.byId.get("message-list"), (n) => n.classList?.contains("worklog-step")).length,
    5,
    "a streaming log never collapses",
  );
  assert.equal(
    walk(document.byId.get("message-list"), (n) => n.classList?.contains("worklog-toggle")).length,
    0,
    "a streaming log offers no collapse control — watching the work is the point",
  );
  emit({ type: "text_delta", delta: "Deck ready." });
  turnDone = true;
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();

  // The finished run is counted once, on the message that produced it (the
  // "5 steps · 1 blocked" receipt asserted below). The rail counts files.
  assert.equal(
    document.byId.get("context-run-line").hidden,
    true,
    "the live line goes down when the turn settles — the story stays on the message",
  );
  assert.equal(document.byId.get("deliverables-meta").textContent, "3 files");
  assert.match(document.byId.get("deliverables-list").textContent, /deck\/outline\.md/);
  // The two files this turn touched are marked; the one that predates it is
  // listed without a mark, because the section is the workspace, not the turn.
  {
    const rows = document.byId.get("deliverables-list").children;
    assert.equal(rows.length, 3, "the section lists the whole workspace");
    assert.match(rows[0].textContent, /deck\/outline\.mdNew/u, "a file this turn wrote is marked");
    assert.doesNotMatch(
      rows[2].textContent,
      /New/u,
      "a file that predates the turn is inventory, not this turn's work",
    );
  }
  assert.equal(
    document.byId.get("context-empty-note").hidden,
    true,
    "with files present the empty line must give way",
  );
  assert.equal(
    document.byId.get("context-deliverables").hidden,
    false,
    "and the section must appear",
  );
  assert.equal(
    document.byId.get("open-workspace-button").hidden,
    false,
    "files the user can see but not open are still a screenshot — the folder must be openable",
  );
  document.byId.get("open-workspace-button").click();
  await settle();
  assert.deepEqual(
    Array.from(revealed),
    ["00000000-0000-4000-8000-0000000d2c01"],
    "reveal must name the selected thread",
  );

  // After the turn, the reconcile repaints the transcript from cache — which
  // stores none of this. The work log must survive on the final assistant
  // message — and, five steps long, it arrives COLLAPSED: a finished log is
  // a receipt, not a narration. Produced rows stay out in the open.
  {
    const strips = walk(document.byId.get("message-list"), (n) => n.classList?.contains("message-worklog"));
    assert.equal(strips.length, 1, "the work log must survive the post-turn repaint");
    const summary = walk(strips[0], (n) => n.classList?.contains("worklog-toggle"));
    assert.equal(summary.length, 1, "a long finished log must offer its summary");
    assert.match(summary[0].textContent, /5 steps · 1 blocked/, "the summary counts steps and refusals");
    assert.equal(
      walk(strips[0], (n) => n.classList?.contains("worklog-step") && !n.classList?.contains("produced")).length,
      0,
      "collapsed means the step rows are gone, not merely styled away",
    );
    const produced = walk(strips[0], (n) => n.classList?.contains("produced"));
    assert.equal(produced.length, 2, "produced rows are a DIFF against the pre-turn workspace, not the inventory");
    assert.match(produced[0].textContent, /Produced.*deck\/outline\.md/su);
    assert.doesNotMatch(strips[0].textContent, /old-draft\.txt/, "a file that predates the turn is not this turn's work");

    // Expand: the full story, denial included. Collapse again: back to the
    // receipt. The toggle must survive its own re-render.
    summary[0].click();
    const expandedStrip = walk(document.byId.get("message-list"), (n) => n.classList?.contains("message-worklog"))[0];
    assert.match(expandedStrip.textContent, /blocked — outside the workspace/su, "expansion shows the denial");
    assert.equal(
      walk(expandedStrip, (n) => n.classList?.contains("worklog-step") && !n.classList?.contains("produced")).length,
      5,
      "expansion shows every step",
    );
    walk(expandedStrip, (n) => n.classList?.contains("worklog-toggle"))[0].click();
    const recollapsed = walk(document.byId.get("message-list"), (n) => n.classList?.contains("message-worklog"))[0];
    assert.equal(
      walk(recollapsed, (n) => n.classList?.contains("worklog-step") && !n.classList?.contains("produced")).length,
      0,
      "and folds back to the receipt",
    );
  }

  // A new turn starts its own story: the last turn's activity is not what
  // this turn is doing.
  input.value = "And now refine it";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();
  // With activity reset, a fresh running turn with no events yet reads
  // "Running" — stale entries would surface as the last tool's name.
  assert.equal(document.byId.get("context-run-line").hidden, false, "a new run raises the line again");
  assert.match(
    document.byId.get("context-run-line").textContent,
    /^Running$/u,
    "activity must reset per turn",
  );

  // One call is one row. The sidecar announces a tool the moment the model
  // asks for it and only then does the guard refuse it or the user decline
  // the approval card — so the denial always arrives as a SECOND frame about
  // a call the log already has. Appending would read as two steps and count
  // as two in the receipt, for a tool that ran zero times.
  const liveSteps = () => {
    const strip = walk(
      document.byId.get("message-list"),
      (n) => n.classList?.contains("message-worklog") && n.classList?.contains("live"),
    )[0];
    return strip
      ? walk(strip, (n) => n.classList?.contains("worklog-step") && !n.classList?.contains("produced"))
      : [];
  };
  emit({ type: "tool_use", name: "Write", target: "draft.md" });
  emit({ type: "tool_denied", name: "Write", target: "draft.md", reason: "用户拒绝了此操作" });
  await settle();
  {
    const steps = liveSteps();
    assert.equal(steps.length, 1, "a denial settles the step it refers to instead of adding one");
    assert.equal(steps[0].classList.contains("denied"), true, "and that step reads as blocked");
    assert.match(steps[0].textContent, /Write.*draft\.md.*blocked/su);
  }
  // A denial with no matching step still has to be visible: the guard can
  // refuse a call the stream never announced.
  emit({ type: "tool_denied", name: "Bash", target: "", reason: "工具不在本地循环的许可面内" });
  await settle();
  assert.equal(liveSteps().length, 2, "an unmatched denial is still its own row");

  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();
}

// The L2 approval loop: the sidecar pauses a tool call and asks. Each request
// becomes a card; the click delivers the decision through the typed bridge;
// the card collapses in place to its outcome; and a turn that ends takes its
// unanswered questions with it. The reasoning stream rides the same turn as a
// one-line live caption that folds into an expandable "Thought" label.
async function testToolApprovalCardsAndReasoningCaption() {
  const approvals = [];
  let emit = () => {};
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-09T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("approval-thread", "Approvals")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "approval-turn" });
        return { turnID: "approval-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
      async approveTurnTool(turnID, input) {
        approvals.push({ turnID, approval_id: input.approval_id, decision: input.decision });
        if (input.approval_id === "ap-2") {
          return typedFailure(404, { error: "approval_not_pending" });
        }
        return typedSuccess({ resolved: true });
      },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Write the file";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  // The live reasoning caption shows the LAST non-empty line, not the stream.
  emit({ type: "reasoning_delta", delta: "Consider the outline.\n" });
  emit({ type: "reasoning_delta", delta: "Plan the write." });
  await settle();
  const captions = walk(document.byId.get("message-list"), (n) => n.classList?.contains("reasoning-caption"));
  assert.equal(captions.length, 1, "reasoning must render one live caption");
  assert.equal(captions[0].textContent, "Thinking… Plan the write.");
  const reasoningStrip = captions[0].parentNode;

  emit({ type: "approval_request", id: "ap-1", name: "Write", target: "a.md" });
  emit({ type: "approval_request", id: "ap-1", name: "Write", target: "a.md" });
  emit({ type: "approval_request", id: "ap-2", name: "Bash", target: "" });
  emit({ type: "approval_request", id: "ap-3", name: "Edit", target: "b.md" });
  await settle();
  const cards = walk(document.byId.get("message-list"), (n) => n.classList?.contains("approval-card"));
  assert.equal(cards.length, 3, "one card per approval id — a duplicated frame must not stack");
  assert.match(cards[0].textContent, /Agent requests to run Write · a\.md/u);
  assert.match(cards[1].textContent, /Agent requests to run Bash/u);
  assert.doesNotMatch(cards[1].textContent, /Bash ·/u, "an absent target must not leave a dangling separator");

  // Three of the four answers are "yes" and they are not the same yes. The
  // two that outlive this call say which TOOL they are about, because the
  // stored grant is keyed by tool name and applies to every target it is ever
  // asked about — not to a.md, which is the only thing the title mentions.
  // Read together, "Write · a.md" over "Always allow Write" is the difference
  // between the call and the grant, stated on the control that makes it.
  const allowButtons = walk(cards[0], (n) => n.classList?.contains("approval-button"));
  assert.equal(allowButtons.length, 4, "the card offers all four decisions");
  assert.deepEqual(
    allowButtons.map((b) => b.textContent),
    ["Allow once", "Allow Write this session", "Always allow Write", "Deny"],
    "a grant that outlives this call has to name what it grants",
  );
  // Weight follows breadth: the narrowest yes and the no are the two coloured
  // answers, and the broad grants are quiet. A permanent grant must not be the
  // easiest thing on the card to press.
  assert.deepEqual(
    allowButtons.map((b) => [...b.classList.values].filter((c) => c !== "approval-button")),
    [["once"], ["broad"], ["broad"], ["deny"]],
  );
  // A question with no tool name still reads as a sentence rather than as a
  // label with a hole in it.
  const bashButtons = walk(cards[1], (n) => n.classList?.contains("approval-button"));
  assert.deepEqual(
    bashButtons.map((b) => b.textContent),
    ["Allow once", "Allow Bash this session", "Always allow Bash", "Deny"],
  );
  allowButtons[0].click();
  allowButtons[0].click();
  await settle();
  assert.deepEqual(
    { ...approvals[0] },
    { turnID: "approval-turn", approval_id: "ap-1", decision: "allow_once" },
    "the click must deliver {approval_id, decision} for the streaming turn",
  );
  assert.equal(approvals.length, 1, "a second click must not send a second decision");
  assert.match(cards[0].textContent, /Allowed once/u);
  assert.equal(
    walk(cards[0], (n) => n.classList?.contains("approval-button")).length,
    0,
    "an answered card is a label, not a disabled form",
  );

  // The second card answers into a 404: the sidecar no longer knows the id,
  // which the renderer reads as expiry, not failure.
  walk(cards[1], (n) => n.classList?.contains("approval-button"))[3].click();
  await settle();
  assert.equal(approvals[1].decision, "deny");
  assert.match(cards[1].textContent, /Expired/u, "a 404 answer must read as expired");

  // The turn ends with ap-3 unanswered: it expires locally, with no bridge
  // call — answering into a finished turn could only 404 anyway.
  emit({ type: "text_delta", delta: "Done writing." });
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();
  assert.match(cards[2].textContent, /Expired/u, "an unanswered card expires when the turn ends");
  assert.equal(approvals.length, 2, "expiry is local — no decision is invented for the user");

  // The caption folded into a toggle that reveals the full pre-wrap text.
  const toggles = walk(reasoningStrip, (n) => n.classList?.contains("reasoning-toggle"));
  assert.equal(toggles.length, 1, "the settled turn must fold the caption into a Thought label");
  assert.match(toggles[0].textContent, /Thought/u);
  // Collapsed, the label carries the gist — read from the HEAD of the
  // reasoning, which is what identifies the subject, and not from the last
  // line, which is what the live caption showed while the turn was running.
  assert.match(
    toggles[0].textContent,
    /Thought · Consider the outline\./u,
    "a collapsed thought must say what it was about, not just that there was one",
  );
  const detail = walk(reasoningStrip, (n) => n.classList?.contains("reasoning-detail"))[0];
  assert.equal(detail.hidden, true, "the full reasoning starts collapsed");
  assert.equal(
    detail.classList.contains("disclosed"),
    true,
    "an expansion hangs off its control on the shared hairline",
  );
  toggles[0].click();
  assert.equal(detail.hidden, false, "the label expands on demand");
  assert.equal(
    toggles[0].textContent,
    "▾ Thought",
    "expanded, the label drops the gist rather than repeating the first line below it",
  );
  assert.equal(detail.textContent, "Consider the outline.\nPlan the write.");
}

// --- A collapsed thought is a sentence, not a label ---------------------------
//
// The summary is plain text inside a button, so markup that a model opened its
// reasoning with must not survive into it, and the line must not be able to
// widen the column it sits in.
async function testThoughtSummaryReadsAsProse() {
  const { ns } = await runRenderer(undefined);
  const { reasoningSummaryLine } = ns;

  assert.equal(reasoningSummaryLine(""), "");
  assert.equal(reasoningSummaryLine("   \n\n  "), "");
  assert.equal(
    reasoningSummaryLine("## Plan\n\nRead the router config first."),
    "Plan Read the router config first.",
    "heading markers are stripped, their words kept",
  );
  assert.equal(
    reasoningSummaryLine("Call `main()` in **server.go** and [see docs](http://x/y)."),
    "Call main() in server.go and see docs.",
    "inline code, emphasis and links keep their text and lose their syntax",
  );
  assert.equal(
    reasoningSummaryLine("```go\nfunc main() {}\n```\nThen wire it up."),
    "Then wire it up.",
    "a fenced block is not a summary of anything",
  );
  assert.equal(
    reasoningSummaryLine("```go\nfunc main() {"),
    "",
    "an unclosed fence — the shape a truncated stream leaves — is stripped too",
  );

  // Length. English backs up to a word boundary; Chinese has none to back up
  // to and must still return a full-length summary rather than an empty one.
  const english = reasoningSummaryLine(
    "The user wants the router configuration inspected before any edit is made to it, so start there.",
  );
  assert.ok(english.length <= 81, `summary too long: ${english.length}`);
  assert.match(english, /…$/u);
  assert.doesNotMatch(english, / …$/u, "the ellipsis attaches to the word, not to a space");
  assert.ok(english.split(" ").length > 8, "an English summary keeps whole words");

  const chinese = reasoningSummaryLine("用户希望先检查路由配置再做修改".repeat(12));
  assert.ok(chinese.length > 60, "Chinese prose has no spaces and must not be trimmed to nothing");
  assert.ok(chinese.length <= 81, `summary too long: ${chinese.length}`);
  assert.match(chinese, /…$/u);

  // The case the word-boundary guard actually exists for, and the one a
  // space-free string cannot reach: Chinese with a stray space near the front.
  // "Trim to the last space" would find that one space and throw the rest of
  // the summary away, so backing up is only allowed when the boundary is near
  // the cut.
  const mixed = reasoningSummaryLine(`OK ${"用户希望先检查路由配置再做修改".repeat(12)}`);
  assert.ok(
    mixed.length > 60,
    `one early space must not collapse the summary: got ${JSON.stringify(mixed)}`,
  );
  assert.ok(mixed.length <= 81, `summary too long: ${mixed.length}`);

  // A summary long enough to be worth having is long enough to blow out the
  // column, so the control clamps and ellipsises rather than growing.
  assert.match(
    rendererCSS,
    /\.reasoning-toggle\s*\{[^}]*?max-width:\s*min\(var\(--measure\), 100%\);[^}]*?text-overflow:\s*ellipsis;/u,
    "the gist may be long; it may not widen the column",
  );
}

// An approval question is about a step the log already shows. The CLI
// announces a tool call before it asks permission for it — assistant message
// first, permission request second, an order no renderer can change — so a
// card of its own put "Agent requests to run Write · outline.md" next to a
// line reading "Write outline.md" that looked like it had already happened.
// The question belongs ON that row: it opens into the ask, and closes back
// into the answer, and the turn ends with as many rows as it made calls.
async function testApprovalBecomesTheStepItIsAbout() {
  const approvals = [];
  let emit = () => {};
  let turnDone = false;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-11T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("00000000-0000-4000-8000-0000000d2c02", "Merged approvals")] });
      }
      if (pathname === "/agent/threads/00000000-0000-4000-8000-0000000d2c02/messages") {
        return response({
          items: turnDone
            ? [{
                uuid: "merge-msg", user_text: "Write the outline", ai_text: "Written.",
                streaming_state: "complete",
                created_at: "2026-08-11T10:00:00Z", updated_at: "2026-08-11T10:00:00Z",
              }]
            : [],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      async listWorkspaceFiles() { return typedSuccess({ items: [], count: 0, truncated: false }); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "merge-turn" });
        return { turnID: "merge-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
      async approveTurnTool(turnID, input) {
        approvals.push({ turnID, approval_id: input.approval_id, decision: input.decision });
        return typedSuccess({ resolved: true });
      },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Write the outline";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  const steps = () =>
    walk(
      document.byId.get("message-list"),
      (n) => n.classList?.contains("worklog-step") && !n.classList?.contains("produced"),
    );
  const cards = () =>
    walk(document.byId.get("message-list"), (n) => n.classList?.contains("approval-card"));
  const buttonsIn = (node) => walk(node, (n) => n.classList?.contains("approval-button"));

  emit({ type: "tool_use", name: "Write", target: "outline.md" });
  emit({ type: "tool_use", name: "Edit", target: "notes.md" });
  await settle();
  assert.equal(steps().length, 2, "two announced calls are two rows");
  assert.equal(steps()[0].classList.contains("awaiting"), false, "an announced call is not yet a question");

  // The hit: same turn, same tool, same target, nothing has settled that
  // step. The card does not appear beside the row — it IS the row.
  emit({ type: "approval_request", id: "ap-1", name: "Write", target: "outline.md" });
  await settle();
  assert.equal(cards().length, 0, "a question that found its step must not also draw a card");
  assert.equal(steps().length, 2, "and must not add a row: one call is one row, question or not");
  assert.equal(steps()[0].classList.contains("awaiting"), true, "the step it is about becomes the question");
  assert.match(steps()[0].textContent, /Write.*outline\.md.*Awaiting approval/su);
  assert.equal(buttonsIn(steps()[0]).length, 4, "the row offers all four decisions");
  assert.equal(buttonsIn(steps()[1]).length, 0, "and only that row");
  assert.match(
    document.byId.get("context-run-line").textContent,
    /Needs your approval.*Write · outline\.md/su,
    "a run blocked on the user must not read as a tool making progress",
  );
  assert.equal(
    document.byId.get("context-run-line").classList.contains("is-blocked"),
    true,
    "and it must be the one thing in the rail that raises its voice",
  );

  // A second question in the same turn is its own question on its own row.
  emit({ type: "approval_request", id: "ap-2", name: "Edit", target: "notes.md" });
  await settle();
  assert.equal(steps().length, 2, "two questions, two rows, still no cards");
  assert.equal(buttonsIn(steps()[1]).length, 4, "the second row carries its own buttons");
  // Merged into a row or standing as a card, a question is the same question,
  // so the broad grants name their tool in both shapes — and each row names
  // ITS tool, not the one above it.
  assert.deepEqual(
    buttonsIn(steps()[1]).map((b) => b.textContent),
    ["Allow once", "Allow Edit this session", "Always allow Edit", "Deny"],
  );

  // Answering resolves that row in place — and touches nothing else.
  buttonsIn(steps()[0])[0].click();
  await settle();
  assert.deepEqual(
    { ...approvals[0] },
    { turnID: "merge-turn", approval_id: "ap-1", decision: "allow_once" },
    "the row's click must deliver {approval_id, decision} for the streaming turn",
  );
  assert.equal(steps().length, 2, "an answer settles the row, it does not add one");
  assert.equal(steps()[0].classList.contains("awaiting"), false, "the answered row stops asking");
  assert.match(steps()[0].textContent, /Write.*outline\.md.*Allowed once/su);
  assert.equal(buttonsIn(steps()[0]).length, 0, "an answered row is a result, not a disabled form");
  assert.equal(buttonsIn(steps()[1]).length, 4, "the other question is untouched");

  // The tool then ran and reported back: the row closes for real.
  emit({ type: "tool_result", name: "Write", target: "outline.md", isError: false });
  await settle();
  assert.equal(steps()[0].classList.contains("done"), true, "a returned tool marks its step finished");
  assert.match(steps()[0].textContent, /Allowed once/u, "and keeps the answer that let it run");

  // The misses, which must keep the card. A question with no step on screen
  // (the guard can refuse a call the stream never announced, and a question
  // can outrun its announcement) has nothing to merge into.
  emit({ type: "approval_request", id: "ap-3", name: "Bash", target: "" });
  await settle();
  assert.equal(cards().length, 1, "a question with no step of its own keeps its card");
  assert.match(cards()[0].textContent, /Agent requests to run Bash/u);
  assert.equal(steps().length, 2, "and invents no row");

  // A second question about a step that already owns one also falls back:
  // one row cannot answer two ids.
  emit({ type: "tool_use", name: "Glob", target: "" });
  emit({ type: "approval_request", id: "ap-4", name: "Glob", target: "" });
  emit({ type: "approval_request", id: "ap-5", name: "Glob", target: "" });
  await settle();
  assert.equal(steps().length, 3, "the Glob call is one row");
  assert.equal(steps()[2].classList.contains("awaiting"), true, "the first Glob question takes the row");
  assert.equal(cards().length, 2, "the second Glob question keeps a card of its own");

  // A settled step is no longer a candidate: the finished Write must not
  // swallow a question about a second write to the same file.
  emit({ type: "approval_request", id: "ap-6", name: "Write", target: "outline.md" });
  await settle();
  assert.equal(cards().length, 3, "a question about a closed step is not that step's question");
  assert.equal(steps()[0].classList.contains("awaiting"), false, "the finished row stays finished");

  // The turn ends with ap-2, ap-4 unanswered on their rows and three cards
  // open: all of them expire, locally, with no decision invented.
  emit({ type: "text_delta", delta: "Written." });
  turnDone = true;
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();
  assert.equal(approvals.length, 1, "expiry is local — no decision is invented for the user");
  const settled = steps();
  assert.equal(settled.length, 3, "the finished log has one row per call");
  assert.match(settled[1].textContent, /Edit.*notes\.md.*Expired/su, "an unanswered row expires in place");
  assert.match(settled[2].textContent, /Glob.*Expired/su);
  assert.equal(
    walk(document.byId.get("message-list"), (n) => n.classList?.contains("approval-button")).length,
    0,
    "a finished turn leaves no button that could only answer into a 404",
  );
  assert.equal(
    walk(document.byId.get("message-list"), (n) => n.classList?.contains("awaiting")).length,
    0,
    "and nothing still asking",
  );
}

// A denial is a second frame about a call the log already has, and it can
// only be folded into that call if it says which call it was — which is why
// the bridge puts the target on tool_denied. Merged questions ride the same
// rule: answering Deny settles the row, and the denial that follows lands on
// it rather than drawing a second one.
async function testDenialFoldsIntoTheRowItAnswers() {
  const approvals = [];
  let emit = () => {};
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-11T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("00000000-0000-4000-8000-0000000d2c03", "Denials")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "deny-turn" });
        return { turnID: "deny-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
      async approveTurnTool(turnID, input) {
        approvals.push({ turnID, approval_id: input.approval_id, decision: input.decision });
        return typedSuccess({ resolved: true });
      },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Write it";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  const steps = () =>
    walk(
      document.byId.get("message-list"),
      (n) => n.classList?.contains("worklog-step") && !n.classList?.contains("produced"),
    );

  emit({ type: "tool_use", name: "Write", target: "secret.md" });
  emit({ type: "approval_request", id: "ap-1", name: "Write", target: "secret.md" });
  await settle();
  walk(steps()[0], (n) => n.classList?.contains("approval-button"))[3].click();
  await settle();
  assert.equal(approvals[0].decision, "deny");
  assert.match(steps()[0].textContent, /Denied/u, "the click reads back before the sidecar answers");

  // The sidecar's denial arrives second, carrying the target the bridge now
  // sends. One call, one row — and the reason replaces the bare decision,
  // because "Denied" on top of "blocked — 用户拒绝了此操作" is one fact twice.
  emit({ type: "tool_denied", name: "Write", target: "secret.md", reason: "用户拒绝了此操作" });
  await settle();
  assert.equal(steps().length, 1, "the denial settles the row it answers instead of adding one");
  assert.equal(steps()[0].classList.contains("denied"), true);
  assert.match(steps()[0].textContent, /blocked — 用户拒绝了此操作/su);
  assert.doesNotMatch(steps()[0].textContent, /Denied/u, "and states the outcome once");
  // The question is answered, so the rail stops asking and goes back to
  // naming the step the loop is on. The refusal itself is told once, inline.
  assert.match(
    document.byId.get("context-run-line").textContent,
    /Step 1.*Write · secret\.md/su,
    "the rail follows the row",
  );
  assert.equal(
    document.byId.get("context-run-line").classList.contains("is-blocked"),
    false,
    "an answered question is no longer blocking",
  );

  // What the real chain actually sends: the CLI feeds a refusal back as an
  // ERRORED tool result, so every denial is followed by a tool_result the
  // renderer has already answered. Read from the live smoke; a denial that
  // then re-read as a bare "failed" would lose the reason the user needs.
  emit({ type: "tool_result", name: "Write", target: "secret.md", isError: true });
  await settle();
  assert.equal(steps().length, 1, "the result of a refused call adds nothing");
  assert.equal(steps()[0].classList.contains("denied"), true, "a settled step stays settled");
  assert.match(steps()[0].textContent, /blocked — 用户拒绝了此操作/su, "and keeps the reason");

  // A tool that ran and failed is not a tool that was blocked: the marks
  // differ, and only refusals count in the receipt.
  emit({ type: "tool_use", name: "Read", target: "gone.md" });
  emit({ type: "tool_result", name: "Read", target: "gone.md", isError: true });
  await settle();
  assert.equal(steps().length, 2);
  assert.equal(steps()[1].classList.contains("failed"), true, "an error result marks the step failed");
  assert.equal(steps()[1].classList.contains("denied"), false, "failing is not being blocked");
  assert.match(steps()[1].textContent, /Read.*gone\.md.*failed/su);

  // A result the log has no open step for settles nothing and adds nothing.
  emit({ type: "tool_result", name: "Grep", target: "", isError: false });
  await settle();
  assert.equal(steps().length, 2, "an unmatched result must not invent a row");

  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();
}

// A first-time user faces an empty screen and a text box. The starter cards
// are the bridge: one click opens the create flow, and once the thread exists
// the card's prompt is waiting in the composer — sending stays the user's
// decision.
async function testStarterPromptLandsInTheComposer() {
  const createCalls = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") return response({ items: [] });
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      async createThread(input) {
        createCalls.push(input.threadUUID);
        return typedSuccess(
          { state: "ready", created: true, thread: createdThread(input.threadUUID, input.name, input.agentMode) },
          201
        );
      },
      startTurn() { throw new Error("a starter card must not auto-send"); },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  const cards = Array.from(document.byId.get("starter-prompts").children).filter(
    (n) => n.classList?.contains("starter-card"),
  );
  assert.equal(cards.length, 3, "the empty state must offer its three starters");
  assert.equal(
    document.byId.get("starter-prompts").hidden,
    false,
    "with the agent available the starters must be visible",
  );

  cards[1].click();
  await settle();
  assert.equal(
    document.byId.get("new-thread-form").hidden,
    false,
    "a starter opens the same create flow the button does",
  );

  document.byId.get("new-thread-form").submit();
  await settle();
  assert.equal(createCalls.length, 1, "the thread must be created");
  assert.match(
    document.byId.get("chat-input").value,
    /product launch deck/i,
    "the card's prompt must be waiting in the composer of the thread it created",
  );

  // The stash is single-use. The composer keeps its draft across selection
  // on purpose (a misclick must not eat typed words), so prove the stash is
  // spent by erasing the box and showing the next create does NOT refill it.
  document.byId.get("chat-input").value = "";
  document.byId.get("chat-input").dispatch("input");
  document.byId.get("new-thread-button").click();
  await settle();
  document.byId.get("new-thread-form").submit();
  await settle();
  assert.equal(createCalls.length, 2);
  assert.equal(
    document.byId.get("chat-input").value,
    "",
    "the starter's prompt is consumed by the thread it created; a plain New must not resurrect it",
  );
}

// And a starter abandoned at the form must not haunt the next create.
async function testCancelledStarterDropsItsPrompt() {
  const { document, ns } = await runRenderer(...(() => {
    const bridge = {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=true") return response({ items: [] });
        if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };
    const desktopBridge = {
      agent: {
        async uploadThreadFile() { throw new Error("not exercised"); },
        async listSkills() { return typedSuccess(pptCatalog()); },
        async createThread(input) {
          return typedSuccess(
            { state: "ready", created: true, thread: createdThread(input.threadUUID, input.name, input.agentMode) },
            201
          );
        },
        startTurn() { throw new Error("not exercised"); },
        async cancelTurn(turnID) { return { turnID, canceled: true }; },
      },
    };
    return [[bridge, desktopBridge]][0];
  })());

  const cards = Array.from(document.byId.get("starter-prompts").children).filter(
    (n) => n.classList?.contains("starter-card"),
  );
  cards[0].click();
  await settle();
  document.byId.get("new-thread-cancel-button").click();
  await settle();

  // Via the empty-state button, deliberately: the sidebar's New clears the
  // stash itself, so only this path would expose a stash that survived the
  // cancel.
  document.byId.get("empty-new-thread-button").click();
  await settle();
  document.byId.get("new-thread-form").submit();
  await settle();
  assert.equal(
    document.byId.get("chat-input").value,
    "",
    "cancelling the starter's form must drop its prompt",
  );
}

// "Selected for the next request": a persisted file can be re-attached to a
// new turn by checking it in the Sources panel. Until this existed, files from
// earlier sessions were display-only — the panel showed them, but only a fresh
// upload could ever reach the model again.
async function testSelectedSourcesRideTheNextTurn() {
  const sentFileIDs = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("src-thread", "Sourced"), thread("other-thread", "Other")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        return typedSuccess({ file_id: 4242 });
      },
      async listSkills() { return typedSuccess(pptCatalog()); },
      async listThreadFiles() {
        return typedSuccess({
          items: [
            { file_id: 7, file_name: "report.pdf", file_size: 1024, file_type: "pdf", mime_type: "application/pdf", on_disk: true, created_at: "2026-05-01T00:00:00Z" },
            { file_id: 8, file_name: "gone.txt", file_size: 10, file_type: "txt", mime_type: "text/plain", on_disk: false, created_at: "2026-05-01T00:00:00Z" },
          ],
          count: 2,
        });
      },
      startTurn(input, callback) {
        sentFileIDs.push(Array.from(input.fileIDs ?? []));
        callback({ type: "text_delta", turnID: `src-turn-${sentFileIDs.length}`, delta: "ok" });
        callback({ type: "done", turnID: `src-turn-${sentFileIDs.length}`, result: { code: "", subtype: "", is_error: false } });
        return { turnID: `src-turn-${sentFileIDs.length}` };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, context, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  const checkboxes = () =>
    walk(document.byId.get("sources-list"), (n) => n.classList?.contains("source-select"));
  assert.equal(
    checkboxes().length,
    1,
    "only the readable persisted file gets a checkbox; missing bytes have nothing to attach",
  );

  const box = checkboxes()[0];
  box.checked = true;
  box.dispatch("change");
  await settle();
  assert.equal(document.byId.get("sources-selected").hidden, false);
  assert.match(document.byId.get("sources-selected").textContent, /1 selected for the next request/);

  // A fresh upload joins the selection: the turn carries the union, deduped.
  ns.uploadThreadFile({ name: "notes.txt", size: 12 });
  await settle();

  const input = document.byId.get("chat-input");
  input.value = "Use both documents";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  assert.deepEqual(
    Array.from(sentFileIDs[0]).sort(),
    [4242, 7],
    "the turn must carry the checked persisted file AND the fresh upload",
  );
  assert.equal(
    document.byId.get("sources-selected").hidden,
    true,
    "the label says 'next request', and this was it — the selection must clear once a turn owns the ids",
  );

  // The next turn goes out clean.
  input.value = "And now without them";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();
  assert.deepEqual(Array.from(sentFileIDs[1]), [], "no selection, no file ids");

  // A selection names one thread's files; switching threads must drop it, or
  // thread A's ids would ride into thread B's next turn.
  const rearm = checkboxes()[0];
  rearm.checked = true;
  rearm.dispatch("change");
  await settle();
  assert.equal(document.byId.get("sources-selected").hidden, false, "precondition: re-armed");
  walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-button"))[1].click();
  await settle();
  assert.equal(
    document.byId.get("sources-selected").hidden,
    true,
    "the selection must not survive a thread switch",
  );
}

// The default thread name is minted before the conversation exists, so it is
// wrong more often than right — and grouping and search key off the title.
// This drives the rename flow end to end and pins its local-only scope.
async function testThreadRenameFlow() {
  const renames = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          items: [
            { ...thread("00000000-0000-4000-8000-00000000e001", "Untitled presentation"), cloud_sync_state: "local" },
            { ...thread("00000000-0000-4000-8000-00000000e002", "Cloud deck"), cloud_sync_state: "synced" },
          ],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      async renameThread(uuid, name) {
        renames.push({ uuid, name });
        return typedSuccess({
          renamed: true,
          thread: {
            uuid, name, agent_mode: "ppt", message_count: 1,
            updated_at: "2026-08-09T00:00:00Z", cloud_sync_state: "local",
          },
        });
      },
      startTurn() { return { turnID: "t" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  const buttons = walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-button"));

  // The synced thread first: reading its title must not offer a rename the
  // sidecar would refuse.
  buttons[1].click();
  await settle();
  assert.equal(
    document.byId.get("rename-thread-button").hidden,
    true,
    "a synced thread's name belongs to the cloud copy; renaming it locally would be undone by sync",
  );

  buttons[0].click();
  await settle();
  assert.equal(document.byId.get("rename-thread-button").hidden, false);

  document.byId.get("rename-thread-button").click();
  await settle();
  assert.equal(document.byId.get("rename-thread-form").hidden, false);
  assert.equal(
    document.byId.get("rename-thread-input").value,
    "Untitled presentation",
    "the form must start from the current name, not empty",
  );

  document.byId.get("rename-thread-input").value = "Q3 board review";
  document.byId.get("rename-thread-form").submit();
  await settle();

  assert.deepEqual(Array.from(renames), [
    { uuid: "00000000-0000-4000-8000-00000000e001", name: "Q3 board review" },
  ]);
  assert.equal(document.byId.get("thread-title").textContent, "Q3 board review");
  assert.match(
    document.byId.get("thread-list").textContent,
    /Q3 board review/,
    "the sidebar entry must repaint from the server's answer",
  );
  assert.equal(document.byId.get("rename-thread-form").hidden, true, "the form closes after saving");

  // Cancel path: open again, change nothing on the wire.
  document.byId.get("rename-thread-button").click();
  document.byId.get("rename-thread-input").value = "discarded edit";
  document.byId.get("rename-thread-cancel").click();
  await settle();
  assert.equal(renames.length, 1, "cancel must not send anything");
  assert.equal(document.byId.get("thread-title").textContent, "Q3 board review");
}

// An answer the user cannot get out of the window is a screenshot. These are
// the affordances that make the chat column usable as a work surface rather
// than a transcript viewer.
async function testMessageActionsCopyAndReuse() {
  const answer = "Here is the query.\n\n```sql\nSELECT 1;\n```\n";
  const written = [];
  const clipboard = { async writeText(text) { written.push(text); } };
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("copy-thread", "Copyable")] });
      }
      if (pathname === "/agent/threads/copy-thread/messages") {
        return response({
          items: [{
            uuid: "copy-msg",
            user_text: "Show me the query",
            ai_text: answer,
            streaming_state: "complete",
            created_at: "2026-05-21T00:00:00Z",
            updated_at: "2026-05-21T00:00:00Z",
          }],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn() { return { turnID: "copy-turn" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge, { clipboard });
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  const buttons = walk(
    document.byId.get("message-list"),
    (n) => n.tagName === "BUTTON" && n.classList?.contains("message-action"),
  );
  const byLabel = (label) => buttons.find((b) => b.textContent === label);

  const copyAnswer = byLabel("Copy answer");
  assert.ok(copyAnswer, "a finished answer must offer a copy");
  copyAnswer.click();
  await settle();
  assert.equal(
    written.at(-1),
    answer,
    "copying an answer must yield the Markdown the model wrote, not the rendered text",
  );

  const copyCode = byLabel("Copy code");
  assert.ok(copyCode, "a code block must offer a copy");
  copyCode.click();
  await settle();
  assert.equal(
    written.at(-1),
    "SELECT 1;",
    "copying code must yield the code alone — no fence, no button label",
  );

  const pre = walk(document.byId.get("message-list"), (n) => n.tagName === "PRE")[0];
  assert.equal(
    pre.textContent,
    "SELECT 1;",
    "the button must not live inside <pre>, or its label becomes part of the code",
  );

  // A user message offers its words back, editable. Not a one-click retry:
  // re-running a prompt verbatim is rarely what someone wants after a bad
  // answer.
  const reuse = byLabel("Edit and resend");
  assert.ok(reuse, "a user message must be reusable");
  reuse.click();
  await settle();
  assert.equal(document.byId.get("chat-input").value, "Show me the query");

  // One action row per message, however many times it is rendered.
  const rows = walk(
    document.byId.get("message-list"),
    (n) => n.classList?.contains("message-actions"),
  );
  assert.equal(rows.length, 2, "one action row for the question and one for the answer");
}

// Where there is no clipboard there is no button. An affordance that silently
// does nothing is worse than its absence.
//
// This has to render real messages to mean anything. The first version of it
// used the missing-bridge run, where the renderer bails before drawing any
// message at all — so it asserted that an empty list contains no buttons, and
// passed with the clipboard check deleted.
async function testMessageActionsAbsentWithoutAClipboard() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("no-clip", "No clipboard")] });
      }
      if (pathname === "/agent/threads/no-clip/messages") {
        return response({
          items: [{
            uuid: "m", user_text: "hi", ai_text: "hello",
            streaming_state: "complete",
            created_at: "2026-05-21T00:00:00Z", updated_at: "2026-05-21T00:00:00Z",
          }],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn() { return { turnID: "t" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };
  // No clipboard option: the shipped shell has one, this run does not.
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  assert.match(
    document.byId.get("message-list").textContent,
    /hello/,
    "precondition: the messages must actually be on screen",
  );
  const copyButtons = walk(
    document.byId.get("message-list"),
    (n) => n.tagName === "BUTTON" && /^Copy/u.test(n.textContent || ""),
  );
  assert.equal(copyButtons.length, 0, "no clipboard, no copy button");
  // "Edit and resend" does not touch the clipboard, so it must survive.
  assert.equal(
    walk(document.byId.get("message-list"), (n) => n.tagName === "BUTTON" && n.textContent === "Edit and resend").length,
    1,
    "reuse does not depend on a clipboard and must still be offered",
  );
}

// When the post-turn cache read fails, the streamed bubble is what stays on
// screen — and it has to carry the same actions as a cached one, because
// renderMessage could not offer them when it ran against empty text.
//
// (A successful reconcile repaints from cache, so that path is covered by the
// test above. This is the one where it does not.)
async function testStreamedAnswerGainsActionsWhenReconcileFails() {
  const written = [];
  const clipboard = { async writeText(text) { written.push(text); } };
  let emit = null;
  let answered = false;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("stream-thread", "Streamed")] });
      }
      if (pathname === "/agent/threads/stream-thread/messages") {
        // Fine on selection, broken once the turn has been answered: the
        // sidecar could not be read back.
        if (!answered) return response({ items: [] });
        return response({ error: "unavailable" }, { ok: false, status: 500 });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "stream-turn" });
        return { turnID: "stream-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge, { clipboard });
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  const input = document.byId.get("chat-input");
  input.value = "Give me one line";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  emit({ type: "text_delta", delta: "**done**" });
  answered = true;
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();

  const copy = walk(
    document.byId.get("message-list"),
    (n) => n.tagName === "BUTTON" && n.textContent === "Copy answer",
  );
  assert.equal(copy.length, 1, "the finished answer must offer exactly one copy");
  copy[0].click();
  await settle();
  assert.equal(written.at(-1), "**done**", "copying yields what the model wrote");
}

// Signed out with a local model configured is a supported way to use this
// app, and until now the renderer did not believe it: the sidecar has served
// unauthenticated turns since L3d, but the composer was gated on a cloud
// session, so the local-first configuration existed only on the server.
function localModeBridge({ localRoute, accounts, binding }) {
  const calls = [];
  const bridge = {
    async fetch(pathname) {
      calls.push(pathname);
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-08-08T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("local-thread", "Offline notes")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() {
        // What the sidecar really answers without a session. If the renderer
        // still depended on this, everything below would be unreachable.
        return { ok: false, status: 401, statusText: "Unauthorized", headers: {}, error: { error: "authentication_required" } };
      },
      async listModes() {
        return typedSuccess({ allowed_modes: ["ppt"], local_route: localRoute, tool_loop: false });
      },
      async listRecoverableTurns() { return typedSuccess({ items: [], count: 0 }); },
      async createThread() { throw new Error("not exercised"); },
      async resumeTurn() { throw new Error("not exercised"); },
      startTurn() { return { turnID: "local-turn" }; },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };
  const accountCalls = { list: 0, created: [], selected: [], renamed: [], deleted: [], logouts: 0 };
  if (accounts) {
    desktopBridge.local = {
      async listAccounts() {
        accountCalls.list += 1;
        // binding is what the sidecar answers alongside the accounts: whether
        // this machine's identity has a cloud account connected to it.
        return typedSuccess({
          items: accounts.slice(),
          count: accounts.length,
          binding: binding ?? { state: "unbound" },
        });
      },
      async createAccount(name) {
        accountCalls.created.push(name);
        const next = { id: accounts.length + 1, name, active: false };
        accounts.push(next);
        return typedSuccess(next, 201);
      },
      async selectAccount(id) {
        accountCalls.selected.push(id);
        for (const account of accounts) account.active = account.id === id;
        return typedSuccess({ selected: true });
      },
      async renameAccount(id, name) {
        accountCalls.renamed.push([id, name]);
        const target = accounts.find((account) => account.id === id);
        if (target) target.name = name;
        return typedSuccess(target ?? { id, name, active: false });
      },
      async deleteAccount(id) {
        accountCalls.deleted.push(id);
        const idx = accounts.findIndex((account) => account.id === id);
        if (idx >= 0) accounts.splice(idx, 1);
        return typedSuccess({ deleted: true, threads: 2, messages: 5, files: 1 });
      },
    };
  }
  return { bridge, desktopBridge, calls, accountCalls };
}

// 登录这块的"本地账户"半边: signed-out with a local route, the sidebar must
// name who you are locally and let you switch — and switching must reload
// the whole session, because every loaded thread belonged to the old uid.
async function testLocalAccountSwitcherSwitchesAndReloads() {
  const accounts = [
    { id: 1, name: "Local", active: true },
    { id: 2, name: "Ming", active: false },
  ];
  const { bridge, desktopBridge, calls, accountCalls } = localModeBridge({
    localRoute: true,
    accounts,
  });
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  const row = document.byId.get("local-account-row");
  assert.equal(row.hidden, false, "signed-out local session must show the account row");
  assert.equal(document.byId.get("local-account-name").textContent, "Local");
  assert.equal(document.byId.get("local-account-avatar").textContent, "L");
  assert.equal(
    document.byId.get("settings-overlay").hidden,
    true,
    "the identities open on demand, not by default",
  );

  row.click();
  await settle();
  assert.equal(
    document.byId.get("settings-overlay").hidden,
    false,
    "clicking the identity row opens Settings",
  );
  assert.equal(
    document.byId.get("settings-panel-account").hidden,
    false,
    "and lands on Account, where the identities live",
  );
  assert.equal(
    document.byId.get("settings-nav-account").getAttribute("aria-current"),
    "page",
    "the section list has to say which section that is",
  );
  const items = walk(
    document.byId.get("local-account-list"),
    (node) => node.classList?.contains("local-account-item"),
  );
  assert.equal(items.length, 2, "every account shows in the switcher");
  assert.match(items[0].textContent, /active/, "the active account is marked");

  const threadFetchesBefore = calls.filter(
    (pathname) => pathname === "/agent/threads?include_paused=true",
  ).length;
  const ming = items.find((item) => item.dataset.accountId === "2");
  ming.click();
  await settle();
  await settle();
  await settle();

  assert.deepEqual(accountCalls.selected, [2], "switching selects exactly the clicked account");
  const threadFetchesAfter = calls.filter(
    (pathname) => pathname === "/agent/threads?include_paused=true",
  ).length;
  assert.ok(
    threadFetchesAfter > threadFetchesBefore,
    "switching accounts must reload the thread list — everything on screen belonged to the previous uid",
  );
  assert.equal(document.byId.get("local-account-name").textContent, "Ming");
  assert.equal(document.byId.get("local-account-avatar").textContent, "M");
  assert.equal(
    document.byId.get("settings-overlay").hidden,
    true,
    "the dialog closes once the switch lands: the session reloads underneath it",
  );
  assert.equal(
    document.byId.get("local-account-row").focused,
    true,
    "and focus goes back to the control that opened it",
  );
}

// The two doors into Settings open it at different sections — the gear at
// Model, the identity row at Account — and the section list moves between them
// without closing anything. Exactly one section is on screen at a time, and
// exactly one nav item claims to be current: two visible panels, or a nav that
// marks a section it is not showing, is the failure this pins.
async function testSettingsSectionsAreOneAtATime() {
  const { bridge, desktopBridge } = localModeBridge({
    localRoute: true,
    accounts: [{ id: 1, name: "Local", active: true }],
  });
  const { document } = await runRenderer(bridge, desktopBridge);
  await settle();

  const panels = {
    model: document.byId.get("model-settings-form"),
    account: document.byId.get("settings-panel-account"),
    appearance: document.byId.get("settings-panel-appearance"),
    about: document.byId.get("settings-panel-about"),
  };
  const navItems = {
    model: document.byId.get("settings-nav-model"),
    account: document.byId.get("settings-nav-account"),
    appearance: document.byId.get("settings-nav-appearance"),
    about: document.byId.get("settings-nav-about"),
  };
  const onlyVisible = (expected) => {
    for (const [name, panel] of Object.entries(panels)) {
      assert.equal(
        panel.hidden,
        name !== expected,
        `${name} must be ${name === expected ? "shown" : "hidden"} while ${expected} is the section`,
      );
      assert.equal(
        navItems[name].getAttribute("aria-current"),
        name === expected ? "page" : null,
        `only ${expected} may be aria-current`,
      );
    }
  };

  document.byId.get("local-account-row").click();
  await settle();
  assert.equal(document.byId.get("settings-overlay").hidden, false, "the row opens settings");
  onlyVisible("account");
  assert.equal(
    navItems.account.focused,
    true,
    "focus moves into the dialog, onto the section it opened at",
  );

  // The gear, with the dialog already open: it retargets the section rather
  // than reopening a dialog that is already there.
  document.byId.get("settings-button").click();
  await settle();
  assert.equal(document.byId.get("settings-overlay").hidden, false);
  onlyVisible("model");

  navItems.about.click();
  await settle();
  onlyVisible("about");

  navItems.appearance.click();
  await settle();
  onlyVisible("appearance");

  // Escape closes, and the section it closed on is the one it reopens at.
  document.dispatchKey({ key: "Escape" });
  await settle();
  assert.equal(document.byId.get("settings-overlay").hidden, true, "Escape closes the dialog");
  document.byId.get("local-account-row").click();
  await settle();
  onlyVisible("account");
}

// Reaching Model through the section list, rather than through the gear, must
// still be reaching a form that knows what is saved. The form is filled when
// the section is SHOWN — the load used to hang off the gear's own handler, so
// "identity row → Model" opened a form full of defaults sitting over a stored
// local route, one Save away from writing those defaults back.
async function testModelSectionLoadsWhenReachedFromTheNav() {
  const { bridge, desktopBridge } = localModeBridge({
    localRoute: true,
    accounts: [{ id: 1, name: "Local", active: true }],
  });
  desktopBridge.settings = {
    async getModelRoute() {
      return typedSuccess({
        preferred_route: "local",
        official_model_id: "",
        local: {
          protocol: "anthropic_compatible",
          base_url: "http://127.0.0.1:11434/v1",
          model_id: "llama3.2",
          api_key_configured: true,
        },
        updated_at: "2026-08-11T00:00:00Z",
      });
    },
    async putModelRoute() { throw new Error("not exercised"); },
    async getModelCatalog() { throw new Error("no catalog without an account"); },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  await settle();

  document.byId.get("local-account-row").click();
  await settle();
  assert.equal(
    document.byId.get("model-base-url").value,
    "",
    "opening at Account must not have touched the model form yet",
  );

  document.byId.get("settings-nav-model").click();
  await settle();
  await settle();

  assert.equal(document.byId.get("model-preferred-route").value, "local");
  assert.equal(
    document.byId.get("model-base-url").value,
    "http://127.0.0.1:11434/v1",
    "the endpoint the sidecar has stored, in the field that would overwrite it",
  );
  assert.equal(document.byId.get("model-id").value, "llama3.2");
  assert.equal(
    document.byId.get("model-local-fields").hidden,
    false,
    "the local route shows the fields it runs on",
  );
  assert.match(
    document.byId.get("model-key-status").textContent,
    /stored in Keychain/i,
    "and says a key is stored rather than showing a blank box that means nothing",
  );
}

async function testLocalAccountCreateDoesNotSwitch() {
  const accounts = [{ id: 1, name: "Local", active: true }];
  const { bridge, desktopBridge, accountCalls } = localModeBridge({
    localRoute: true,
    accounts,
  });
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  document.byId.get("local-account-row").click();
  await settle();
  document.byId.get("local-account-name-input").value = "  Ming  ";
  document.byId.get("local-account-create-form").submit();
  await settle();
  await settle();

  assert.deepEqual(accountCalls.created, ["Ming"], "the typed name is trimmed and sent once");
  assert.deepEqual(
    accountCalls.selected,
    [],
    "creating an account must NOT activate it — appearing and taking over are different consents",
  );
  assert.equal(
    document.byId.get("local-account-name").textContent,
    "Local",
    "the active identity is unchanged after a create",
  );
  const items = walk(
    document.byId.get("local-account-list"),
    (node) => node.classList?.contains("local-account-item"),
  );
  assert.equal(items.length, 2, "the new account shows in the switcher, ready to be chosen");
  assert.match(
    document.byId.get("status-card").textContent,
    /Created "Ming"/,
    "the user is told what happened and what to do next",
  );
}

async function testLocalAccountRenameIsALabelChange() {
  const accounts = [
    { id: 1, name: "Local", active: true },
    { id: 2, name: "Ming", active: false },
  ];
  const { bridge, desktopBridge, accountCalls } = localModeBridge({
    localRoute: true,
    accounts,
  });
  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  document.byId.get("local-account-row").click();
  await settle();
  const renameButtons = walk(
    document.byId.get("local-account-list"),
    (node) => node.classList?.contains("local-account-action") && node.textContent === "Rename",
  );
  assert.equal(renameButtons.length, 2, "every account can be renamed, active included");
  renameButtons[1].click();
  await settle();

  const input = walk(
    document.byId.get("local-account-list"),
    (node) => node.tagName === "INPUT",
  )[0];
  assert.ok(input, "rename swaps the row into an inline form");
  assert.equal(input.value, "Ming", "the form starts from the current name");
  input.value = "  明  ";
  // The auth poll repaints the sidebar every second; a repaint mid-edit must
  // not eat the half-typed name. (Found live by the first rename E2E.)
  ns.updateComposerState();
  const inputAfterRepaint = walk(
    document.byId.get("local-account-list"),
    (node) => node.tagName === "INPUT",
  )[0];
  assert.ok(inputAfterRepaint, "a background repaint must not destroy the rename form");
  assert.equal(
    inputAfterRepaint.value,
    "  明  ",
    "the half-typed name survives the repaint",
  );
  const form = walk(
    document.byId.get("local-account-list"),
    (node) => node.tagName === "FORM",
  )[0];
  form.submit();
  await settle();
  await settle();

  assert.deepEqual(accountCalls.renamed, [[2, "明"]], "the trimmed name is sent to the right account");
  assert.deepEqual(accountCalls.selected, [], "renaming must not switch accounts");
  const labels = walk(
    document.byId.get("local-account-list"),
    (node) => node.classList?.contains("local-account-item"),
  ).map((node) => node.textContent);
  assert.ok(labels.some((label) => label === "明"), "the switcher shows the new name");
}

async function testLocalAccountDeleteIsArmedAndScoped() {
  const accounts = [
    { id: 1, name: "Local", active: true },
    { id: 2, name: "Ming", active: false },
  ];
  const { bridge, desktopBridge, accountCalls } = localModeBridge({
    localRoute: true,
    accounts,
  });
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  document.byId.get("local-account-row").click();
  await settle();
  const deleteButtons = walk(
    document.byId.get("local-account-list"),
    (node) => node.classList?.contains("local-account-action") && node.classList?.contains("danger"),
  );
  assert.equal(
    deleteButtons.length,
    1,
    "only the inactive account offers Delete — deleting who you are right now must be impossible",
  );

  // Both clicks in one tick: the VM maps setTimeout to setImmediate, so the
  // 4s disarm fires the moment the test yields. Arming is synchronous anyway.
  deleteButtons[0].click();
  assert.deepEqual(
    accountCalls.deleted,
    [],
    "the first click only arms: no single misclick destroys an identity and its data",
  );
  assert.match(deleteButtons[0].textContent, /Delete all its data\?/);
  deleteButtons[0].click();
  await settle();
  await settle();
  assert.deepEqual(accountCalls.deleted, [2], "the second click deletes exactly the armed account");
  const labels = walk(
    document.byId.get("local-account-list"),
    (node) => node.classList?.contains("local-account-item"),
  ).map((node) => node.textContent);
  assert.equal(labels.length, 1, "the deleted account leaves the switcher");
  assert.match(
    document.byId.get("status-card").textContent,
    /Deleted "Ming" and its 2 conversations/,
    "the user is told what left with the identity",
  );
}

// Version skew (stale sidecar + newer renderer, or vice versa) used to be
// indistinguishable from "not signed in": listModes answered, the strict
// parse failed, and the app silently showed the sign-in wall. The failure
// must name itself.
// The composer context chips: where the turn runs and who it runs as, at
// the place the user is typing. The dispatch already knows both facts;
// hiding them in settings made "why did this answer come from there?" a
// support question instead of a glance.
async function testComposerChipsNameRuntimeAndIdentity() {
  const accounts = [
    { id: 1, name: "Local", active: false },
    { id: 2, name: "Ming", active: true },
  ];
  const { bridge, desktopBridge } = localModeBridge({ localRoute: true, accounts });
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button"),
  )[0];
  threadButton.click();
  await settle();

  const runtime = document.byId.get("runtime-chip");
  assert.equal(runtime.hidden, false, "a usable composer must say where the turn runs");
  assert.match(runtime.textContent, /Local · chat/, "chat-only local mode is named as such");
  const accountChip = document.byId.get("account-chip");
  assert.equal(accountChip.hidden, false, "local-only sessions name the identity at the composer");
  assert.equal(accountChip.textContent, "Ming");
  const modeChip = document.byId.get("mode-chip");
  assert.equal(modeChip.hidden, false, "a single skill is shown as a chip, not a one-option selector");
  assert.equal(modeChip.textContent, "PPT");
}

// Export: the conversation leaves as a Markdown file in the thread's own
// workspace, and the folder opens — "your data can leave when you say so"
// ends with the file in front of the user.
// Pins: the sidebar's Pinned group leads, and the toggle round-trips through
// the sidecar so the sidebar shows what the server would answer, not a local
// guess at it.
// ⌘K is a command palette, not just a switcher: doing things stays on the
// keyboard. Commands are context-aware — a dead-end action never shows.
// Content search: the title filter answers instantly from memory; message
// bodies are the sidecar's to search. The two layers must compose without
// the async one ever lying about which query it answers.
// First run, neither signed in nor a local model configured: the two ways
// of working are presented as equals, and each card leads somewhere real.
async function testOnboardingPathsLeadSomewhere() {
  let beginCalls = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-08-08T00:00:00Z" });
      }
      if (pathname === "/settings/model-route") {
        return response({
          preferred_route: "official",
          local: { protocol: "", base_url: "", model_id: "", api_key_configured: false },
          updated_at: "2026-08-08T00:00:00Z",
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const { desktopBridge } = localModeBridge({ localRoute: false });
  desktopBridge.settings = {
    async getModelRoute() {
      return typedSuccess({
        preferred_route: "official",
        local: { protocol: "", base_url: "", model_id: "", api_key_configured: false },
        updated_at: "2026-08-08T00:00:00Z",
      });
    },
    async putModelRoute() { throw new Error("not exercised"); },
  };
  desktopBridge.auth = {
    async beginLogin() {
      beginCalls += 1;
      return { state: "awaiting_password" };
    },
    async loginStatus() { return { state: "idle" }; },
    async submitLoginPassword() { throw new Error("not exercised"); },
    async cancelLogin() { return { state: "idle" }; },
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  assert.equal(document.byId.get("onboarding-paths").hidden, false, "first run shows both paths");

  // The local path opens the Models form — the actual next step, not a hint.
  document.byId.get("onboarding-local").click();
  await settle();
  assert.equal(
    document.byId.get("model-settings-form").hidden,
    false,
    "choosing local opens the model settings form",
  );

  // The sign-in path starts the real login transaction.
  document.byId.get("onboarding-signin").click();
  await settle();
  assert.equal(beginCalls, 1, "choosing sign-in begins the login transaction");
}

async function testOnboardingHiddenOnceUsable() {
  const { bridge, desktopBridge } = localModeBridge({ localRoute: true });
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();
  assert.equal(
    document.byId.get("onboarding-paths").hidden,
    true,
    "a usable app must not re-ask a question the user already answered",
  );
}

// Signed-out local create: the form opens AND submits. The two predicates
// (canOpenNewThread and updateNewThreadState) once disagreed — the form
// opened but the submit stayed disabled forever. One predicate now, tested.
// The BUILT bridge lib versus the sidecar's actual local-create response.
// Every VM scenario fakes the bridge, so this contract had no test between
// the Go suite (server half) and the fakes (renderer half) — and it broke
// twice invisibly: the validator predated cloud_sync_state="local", then
// predated "pinned". The packaged-app smoke found it; this keeps it found.
async function testBridgeLibAcceptsLocalCreateContract() {
  const libCode = fs.readFileSync(
    new URL("../renderer/en/desktop/lib/desktop-bridge.js", import.meta.url),
    "utf8"
  );
  const sandbox = { window: {}, console, Headers, TextEncoder };
  vm.createContext(sandbox);
  vm.runInContext(libCode, sandbox);
  const { createDesktopBridge } = sandbox.window.__workmaxDesktopBridge;
  const respond = (body) => ({
    request: async () => ({
      ok: true, status: 201, statusText: "Created", headers: {},
      text: async () => JSON.stringify(body),
    }),
  });
  const localThread = {
    uuid: "00000000-0000-4000-8000-0000000000e3",
    name: "probe",
    agent_mode: "ppt",
    message_count: 0,
    updated_at: "2026-08-09T15:56:52.103724Z",
    cloud_sync_state: "local",
    pinned: false,
  };
  const input = { threadUUID: localThread.uuid, name: "probe", agentMode: "ppt" };

  const good = await createDesktopBridge(
    respond({ state: "ready", created: true, thread: localThread })
  ).agent.createThread(input);
  assert.equal(good.ok, true);
  assert.equal(
    good.data.thread.cloud_sync_state,
    "local",
    "the bridge must accept the sidecar's signed-out local create verbatim",
  );

  // And it must still REJECT what it does not know — accepting everything
  // would be a different way of having no contract.
  await assert.rejects(
    createDesktopBridge(
      respond({ state: "ready", created: true, thread: { ...localThread, cloud_sync_state: "bogus" } })
    ).agent.createThread(input),
    /malformed/,
    "an unknown sync state must still be refused",
  );
}

async function testSignedOutLocalCanCreateThread() {
  const createCalls = [];
  const newUUID = "00000000-0000-4000-8000-00000000c001";
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-08-08T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [] });
      }
      if (pathname === `/agent/threads/${newUUID}/messages`) {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const { desktopBridge } = localModeBridge({ localRoute: true });
  desktopBridge.agent.createThread = async (input) => {
    createCalls.push(input.threadUUID);
    // cloud_sync_state "local" — what the sidecar actually answers for a
    // signed-out create. The fixture must not be politer than the server.
    return typedSuccess(
      {
        state: "ready",
        created: true,
        thread: { ...createdThread(input.threadUUID, input.name, input.agentMode), cloud_sync_state: "local" },
      },
      201
    );
  };
  const realRandomUUID = globalThis.crypto?.randomUUID;
  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();
  // generateThreadUUID reads globalThis.crypto.randomUUID, so pinning the
  // fake crypto pins the identity — and exercises the real function rather
  // than replacing it.
  context.crypto.randomUUID = () => newUUID;

  document.byId.get("empty-new-thread-button").click();
  await settle();
  assert.equal(document.byId.get("new-thread-form").hidden, false, "the form opens signed-out");
  const nameInput = document.byId.get("new-thread-name");
  nameInput.value = "EA 冒烟";
  nameInput.dispatch("input");
  assert.equal(
    document.byId.get("new-thread-submit-button").disabled,
    false,
    "a valid name must enable the submit — the form and its button obey ONE predicate",
  );
  document.byId.get("new-thread-form").submit();
  await settle();
  await settle();
  assert.deepEqual(createCalls, [newUUID], "the create goes out");
  assert.equal(document.byId.get("thread-panel").hidden, false, "the new conversation opens");
  if (realRandomUUID) globalThis.crypto.randomUUID = realRandomUUID;
}

// Message bodies, in the palette. They arrive a debounce and a round trip
// after the keystroke that asked for them, which is why they are the LAST
// group: a group that appeared in the middle would move every row under it,
// and the highlight sitting on one of those rows would end up pointing at
// something the reader never chose.
async function testPaletteSearchesMessageBodies() {
  const searchCalls = [];
  let pendingResolve = null;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-08-08T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("local-thread", "Offline notes"), thread("other-thread", "Other work")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const { desktopBridge } = localModeBridge({ localRoute: true });
  desktopBridge.agent.searchMessages = async (query) => {
    searchCalls.push(query);
    if (query === "slowquery") {
      return new Promise((resolve) => {
        pendingResolve = () =>
          resolve(typedSuccess({
            items: [{ thread_uuid: "other-thread", thread_name: "STALE", role: "you", snippet: "stale answer", created_at: "2026-08-09T00:00:00Z" }],
            count: 1,
          }));
      });
    }
    return typedSuccess({
      items: [{
        thread_uuid: "other-thread",
        thread_name: "Other work",
        role: "assistant",
        snippet: "…churn 环比持平于 2.1%…",
        created_at: "2026-08-09T00:00:00Z",
      }],
      count: 1,
    });
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  const matches = () =>
    walk(
      document.byId.get("quick-switcher-list"),
      (n) => n.classList?.contains("quick-switcher-match"),
    );

  document.dispatchKey({ key: "k", metaKey: true });
  const input = document.byId.get("quick-switcher-input");
  input.value = "c";
  input.dispatch("input");
  await settle();
  await settle();
  assert.deepEqual(searchCalls, [], "one character is not a question worth asking a database");

  input.value = "churn";
  input.dispatch("input");
  await settle();
  await settle();

  assert.deepEqual(searchCalls, ["churn"], "typing asks the sidecar once (debounced)");
  const headings = () =>
    walk(
      document.byId.get("quick-switcher-list"),
      (n) => n.classList?.contains("quick-switcher-heading"),
    ).map((n) => n.textContent);
  assert.deepEqual(
    headings(),
    ["In messages"],
    "bodies are a group of their own, named where the names group is not",
  );
  const match = matches()[0];
  assert.match(match.textContent, /churn 环比持平/, "the snippet is shown");
  assert.match(match.textContent, /Other work/, "the owning conversation is named");

  // Order, with all three groups present: names, then actions, then bodies.
  // Bodies last is not a taste call — they arrive after the list is already
  // drawn, and a group inserted ABOVE the highlight moves whatever Enter is
  // pointing at.
  input.value = "model";
  input.dispatch("input");
  await settle();
  await settle();
  assert.deepEqual(
    headings(),
    ["Actions", "In messages"],
    "the group that arrives late must arrive at the bottom",
  );

  input.value = "churn";
  input.dispatch("input");
  await settle();
  await settle();
  matches()[0].click();
  await settle();
  assert.match(
    document.byId.get("thread-title").textContent,
    /Other work/,
    "choosing a body hit opens the conversation it was said in",
  );
  assert.equal(
    document.byId.get("quick-switcher").hidden,
    true,
    "and closes the palette, like every other choice in it",
  );

  // Generation guard: a SLOW answer to an old query arrives after a newer
  // query already rendered — the stale answer must be dropped.
  document.dispatchKey({ key: "k", metaKey: true });
  input.value = "slowquery";
  input.dispatch("input");
  await settle();
  input.value = "churn";
  input.dispatch("input");
  await settle();
  await settle();
  const resolveStale = pendingResolve;
  if (resolveStale) resolveStale();
  await settle();
  await settle();
  assert.ok(
    matches().every((node) => !node.textContent.includes("STALE")),
    "a late answer to an old query must never overwrite the newer results",
  );
  assert.equal(matches().length, 1, "precondition: the newer answer is what is on screen");

  // Clearing the query clears the group.
  input.value = "";
  input.dispatch("input");
  await settle();
  assert.equal(matches().length, 0, "no query, no bodies");

  // And closing the palette drops what it was holding: an answer to a question
  // the reader has already dismissed must not be waiting there next time.
  input.value = "churn";
  input.dispatch("input");
  await settle();
  await settle();
  assert.equal(matches().length, 1, "precondition: results are on screen");
  document.dispatchKey({ key: "Escape" });
  document.dispatchKey({ key: "k", metaKey: true });
  assert.equal(matches().length, 0, "the palette opens on the query you are about to type");
}

async function testPaletteRunsCommands() {
  let pinned = false;
  const pinCalls = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-08-08T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          // Zero messages ON PURPOSE: the Export guard below is only a real
          // test if the export bridge exists AND the count is what blocks it.
          items: [{ ...thread("local-thread", "Offline notes", 0), pinned }],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const { desktopBridge } = localModeBridge({ localRoute: true });
  desktopBridge.agent.exportThread = async () => {
    throw new Error("export must not run in this scenario");
  };
  desktopBridge.agent.pinThread = async (uuid) => {
    pinCalls.push(uuid);
    pinned = true;
    return typedSuccess({ pinned: true });
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  // Select the thread so thread-scoped commands have a subject.
  walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-button"))[0].click();
  await settle();

  document.dispatchKey({ key: "k", metaKey: true });
  const headings = walk(
    document.byId.get("quick-switcher-list"),
    (n) => n.classList?.contains("quick-switcher-heading"),
  );
  assert.equal(headings.length, 1, "commands sit under their own Actions heading");
  const commandLabels = () =>
    walk(document.byId.get("quick-switcher-list"), (n) => n.classList?.contains("quick-switcher-command"))
      .map((n) => n.textContent);
  assert.ok(
    commandLabels().some((label) => label.startsWith("Pin this conversation")),
    "a selected thread offers Pin",
  );
  assert.ok(
    !commandLabels().some((label) => label.startsWith("Export")),
    "a thread with no messages must NOT offer Export — the sidecar would 409 it",
  );

  const input = document.byId.get("quick-switcher-input");
  input.value = "pin";
  input.dispatch("input");
  const narrowed = walk(
    document.byId.get("quick-switcher-list"),
    (n) => n.classList?.contains("quick-switcher-command") || n.classList?.contains("quick-switcher-item"),
  );
  assert.equal(narrowed.length, 1, "the query narrows across threads AND commands");
  input.dispatch("keydown", { key: "Enter" });
  await settle();
  await settle();
  assert.deepEqual(pinCalls, ["local-thread"], "Enter runs the highlighted command");
  assert.equal(document.byId.get("quick-switcher").hidden, true, "running a command closes the palette");
}

async function testPaletteOpensWithoutThreads() {
  const { bridge, desktopBridge } = localModeBridge({ localRoute: true });
  bridge.fetch = async (pathname) => {
    if (pathname === "/auth/status") {
      return response({ state: "unauthenticated", updated_at: "2026-08-08T00:00:00Z" });
    }
    if (pathname === "/agent/threads?include_paused=true") return response({ items: [] });
    if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
    throw new Error(`unexpected fetch path ${pathname}`);
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();
  document.dispatchKey({ key: "k", metaKey: true });
  assert.equal(
    document.byId.get("quick-switcher").hidden,
    false,
    "commands make the palette useful before the first conversation exists",
  );
  assert.ok(
    walk(document.byId.get("quick-switcher-list"), (n) => n.classList?.contains("quick-switcher-command"))
      .length > 0,
    "actions are offered even with zero threads",
  );
}

// --- Folding the columns, from the buttons -----------------------------------
//
// Both folds are one attribute on <html> and the aria states of the one toggle
// that owns each, which is all this stub can see — the CSS that reads the
// attribute is pinned as text at the top of this file. What is checked here is
// the half that is code: that the attribute is written and cleared, and that
// the toggle says what pressing it will do next. Focus needs no checking: both
// toggles live in the title bar, outside the columns they hide.
async function testColumnFolds() {
  const { bridge, desktopBridge } = localModeBridge({ localRoute: true });
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  const root = document.documentElement;
  const collapse = document.byId.get("sidebar-collapse-button");
  assert.equal(root.getAttribute("data-sidebar"), null, "the rail starts open");
  assert.equal(collapse.getAttribute("aria-expanded"), "true");

  collapse.click();
  assert.equal(root.getAttribute("data-sidebar"), "collapsed", "the rail folds");
  assert.equal(collapse.getAttribute("aria-expanded"), "false");
  assert.equal(
    collapse.getAttribute("aria-label"),
    "Show sidebar",
    "one toggle names the act it will perform, not the state it is looking at",
  );

  collapse.click();
  assert.equal(root.getAttribute("data-sidebar"), null, "and unfolds from the same control");
  assert.equal(collapse.getAttribute("aria-expanded"), "true");
  assert.equal(collapse.getAttribute("aria-label"), "Hide sidebar");

  // The binding every editor on this desktop already uses for this act.
  document.dispatchKey({ key: "\\", metaKey: true });
  assert.equal(root.getAttribute("data-sidebar"), "collapsed", "⌘\\ folds the rail");
  document.dispatchKey({ key: "\\", metaKey: true });
  assert.equal(root.getAttribute("data-sidebar"), null, "and unfolds it");

  // The right column. Two buttons for one column, because the column has two
  // occupants — and each button is a toggle of ITS panel, so the third state
  // (nothing in the column) is what you get by turning the current one off.
  const panel = document.byId.get("context-panel-button");
  assert.equal(root.getAttribute("data-right-panel"), null, "the workspace panel starts in it");
  assert.equal(panel.getAttribute("aria-expanded"), "true");
  assert.equal(panel.getAttribute("aria-label"), "Hide workspace panel");

  panel.click();
  assert.equal(root.getAttribute("data-right-panel"), "none");
  assert.equal(panel.getAttribute("aria-expanded"), "false");
  assert.equal(panel.getAttribute("aria-label"), "Show workspace panel");
  assert.equal(panel.getAttribute("title"), "Show workspace panel");

  // Selecting a conversation must not quietly reopen it. The automatic rule
  // ("no run to project, no column") and this one do not argue: the automatic
  // rule decides whether there is anything to show, this one decides whether
  // the reader wants it, and a choice that a click elsewhere undoes is not a
  // choice.
  walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-button"))[0].click();
  await settle();
  assert.equal(
    root.getAttribute("data-right-panel"),
    "none",
    "a manual close survives opening a conversation",
  );

  panel.click();
  assert.equal(root.getAttribute("data-right-panel"), null, "and pressing again brings it back");
}

// The right column holds one panel at a time, and the two that share it are
// not the same kind of thing: the workspace projects the open conversation,
// the mind belongs to the identity and outlives every conversation. What is
// checked here is that one column really is one column — that the two switches
// displace each other rather than stacking — and that the mind's independence
// from the conversation survives being moved into a column that folds with it.
async function testRightColumnHoldsOnePanelAtATime() {
  const harness = mindBridge();
  const { document } = await runRenderer(harness.bridge, harness.desktopBridge);
  await settle();

  const root = document.documentElement;
  const brain = document.byId.get("mind-button");
  const workspace = document.byId.get("context-panel-button");

  // This harness has no conversations at all, so the workspace panel has
  // nothing to project — and the mind is still one press away. That is the
  // whole reason the mind is exempt from the fold the workspace obeys.
  assert.equal(root.getAttribute("data-right-panel"), null);
  assert.equal(brain.getAttribute("aria-expanded"), "false");
  assert.equal(brain.getAttribute("aria-label"), "Show mind panel");

  brain.click();
  await settle();
  assert.equal(root.getAttribute("data-right-panel"), "mind", "the brain puts the mind in the column");
  assert.equal(brain.getAttribute("aria-expanded"), "true");
  assert.equal(brain.getAttribute("aria-label"), "Hide mind panel");
  assert.equal(brain.getAttribute("title"), "Hide mind panel");
  assert.ok(harness.calls.list > 0, "and showing it reads the roster from the sidecar");
  assert.equal(
    workspace.getAttribute("aria-expanded"),
    "false",
    "the other switch must say its panel is no longer the one in the column",
  );
  // Focus follows it in: the panel is the last thing in the DOM and its switch
  // is in the title bar, so a keyboard reader would otherwise have to walk the
  // rail and the whole transcript to reach what they just asked for.
  assert.equal(document.byId.get("mind-panel").focused, true);

  // The other switch swaps the occupant rather than stacking a second panel.
  workspace.click();
  assert.equal(root.getAttribute("data-right-panel"), null, "the workspace takes the column back");
  assert.equal(brain.getAttribute("aria-expanded"), "false");
  assert.equal(workspace.getAttribute("aria-expanded"), "true");

  // Pressing the brain again is an UNDO of pressing it, not a fold: the column
  // goes back to what the mind interrupted. Otherwise glancing at the mind
  // would quietly close a workspace column the reader never asked to close.
  brain.click();
  await settle();
  brain.click();
  assert.equal(
    root.getAttribute("data-right-panel"),
    null,
    "putting the mind away hands the column back to what it was showing",
  );

  // And what it hands back can be nothing at all — a reader who had already
  // folded the column must not find it reopened by a trip to the mind.
  workspace.click();
  assert.equal(root.getAttribute("data-right-panel"), "none", "precondition: folded");
  brain.click();
  await settle();
  assert.equal(root.getAttribute("data-right-panel"), "mind");
  brain.click();
  assert.equal(root.getAttribute("data-right-panel"), "none", "and folded is what it goes back to");
  assert.equal(brain.getAttribute("aria-expanded"), "false");
  assert.equal(workspace.getAttribute("aria-expanded"), "false");
}


// The rail's search icon opens the one palette, and does not grow a second
// search of its own on the way.
async function testSidebarSearchIconOpensThePalette() {
  const { bridge, desktopBridge } = localModeBridge({ localRoute: true });
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  const button = document.byId.get("sidebar-search-button");
  assert.equal(document.byId.get("quick-switcher").hidden, true);
  button.click();
  assert.equal(document.byId.get("quick-switcher").hidden, false, "the icon opens the palette");
  assert.equal(
    document.byId.get("quick-switcher-input").focused,
    true,
    "and puts the caret where the query goes",
  );
  button.click();
  assert.equal(document.byId.get("quick-switcher").hidden, true, "pressing it again dismisses");
}

async function testPinnedThreadsLeadTheSidebar() {
  let pinned = false;
  const pinCalls = [];
  const unpinCalls = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "unauthenticated", updated_at: "2026-08-08T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        const rows = [
          { ...thread("fresh-thread", "Fresh work"), updated_at: new Date().toISOString() },
          { ...thread("old-thread", "Old favourite"), updated_at: "2026-07-01T00:00:00Z", pinned },
        ];
        // The sidecar sorts pinned first; the fake must honour the contract.
        rows.sort((a, b) => (b.pinned === true) - (a.pinned === true));
        return response({ items: rows });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const { desktopBridge } = localModeBridge({ localRoute: true });
  desktopBridge.agent.pinThread = async (uuid) => {
    pinCalls.push(uuid);
    pinned = true;
    return typedSuccess({ pinned: true });
  };
  desktopBridge.agent.unpinThread = async (uuid) => {
    unpinCalls.push(uuid);
    pinned = false;
    return typedSuccess({ pinned: false });
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  const groupLabels = () =>
    walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-group"))
      .map((n) => n.textContent);
  assert.deepEqual(groupLabels(), ["Today", "Older"], "no pins, no Pinned group");

  const pinButtons = walk(
    document.byId.get("thread-list"),
    (n) => n.classList?.contains("thread-pin"),
  );
  assert.equal(pinButtons.length, 2, "every row offers Pin");
  // Pin the old thread (second row).
  pinButtons[1].click();
  await settle();
  await settle();

  assert.deepEqual(pinCalls, ["old-thread"]);
  assert.equal(
    groupLabels()[0],
    "Pinned",
    "a pinned thread must lead the sidebar under its own heading",
  );
  const firstTitle = walk(
    document.byId.get("thread-list"),
    (n) => n.classList?.contains("thread-button"),
  )[0].textContent;
  assert.match(firstTitle, /Old favourite/, "the pin overrides the calendar");

  // Unpin from the (now first) row's toggle.
  const unpinButton = walk(
    document.byId.get("thread-list"),
    (n) => n.classList?.contains("thread-pin"),
  )[0];
  assert.equal(unpinButton.textContent, "Unpin", "the toggle names its next action");
  unpinButton.click();
  await settle();
  await settle();
  assert.deepEqual(unpinCalls, ["old-thread"]);
  assert.deepEqual(groupLabels(), ["Today", "Older"], "unpinning restores the calendar");
}

async function testExportThreadWritesAndReveals() {
  const accounts = [{ id: 1, name: "Local", active: true }];
  const { bridge, desktopBridge } = localModeBridge({ localRoute: true, accounts });
  const exportCalls = [];
  const revealCalls = [];
  desktopBridge.agent.exportThread = async (uuid) => {
    exportCalls.push(uuid);
    return typedSuccess({ exported: true, path: "exports/conversation-x.md", messages: 6, bytes: 812 });
  };
  desktopBridge.agent.revealWorkspace = async (uuid) => {
    revealCalls.push(uuid);
    return typedSuccess({ revealed: true });
  };
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button"),
  )[0];
  threadButton.click();
  await settle();

  const exportButton = document.byId.get("export-thread-button");
  assert.equal(exportButton.hidden, false, "a conversation with messages offers Export");
  exportButton.click();
  await settle();
  await settle();

  assert.equal(exportCalls.length, 1, "one click, one export");
  assert.deepEqual(revealCalls, exportCalls, "the folder opens on the exported thread");
  assert.match(
    document.byId.get("status-card").textContent,
    /Exported 6 messages/,
    "the user is told what was written",
  );
}

async function testComposerAccountChipSkipsTheDefaultIdentity() {
  const accounts = [{ id: 1, name: "Local", active: true }];
  const { bridge, desktopBridge } = localModeBridge({ localRoute: true, accounts });
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();
  // The subtitle carries where the turn runs, which is the fact the permanent
  // status block used to carry. One line, on the identity it describes.
  assert.equal(
    document.byId.get("local-account-hint").textContent,
    "Local model · this machine",
    "the local route is ambient state, so it belongs next to the identity",
  );
  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button"),
  )[0];
  threadButton.click();
  await settle();
  assert.equal(
    document.byId.get("account-chip").hidden,
    true,
    'the default identity is nobody — naming it would just say "Local" twice',
  );
}

async function testModesParseFailureNamesTheSkew() {
  const { bridge, desktopBridge } = localModeBridge({ localRoute: true });
  desktopBridge.agent.listModes = async () =>
    typedSuccess({ allowed_modes: ["ppt"], totally_unexpected_shape: true });
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();
  assert.match(
    document.byId.get("status-card").textContent,
    /out of sync/i,
    "a modes answer the UI cannot parse must be reported as skew, not silence",
  );
}

// The identity exists before any model does. A machine with no account and no
// local model is still SOMEBODY's — the switcher used to hide itself here,
// which said the opposite.
async function testLocalIdentityIsNamedWithoutAnyModel() {
  const accounts = [
    { id: 1, name: "Ming", active: true },
    { id: 2, name: "Work", active: false },
  ];
  const { bridge, desktopBridge } = localModeBridge({ localRoute: false, accounts });
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();
  assert.equal(
    document.byId.get("local-account-row").hidden,
    false,
    "you are someone on this machine before you connect anything",
  );
  assert.equal(document.byId.get("local-account-name").textContent, "Ming");
  // The second line describes the identity's situation. It used to read
  // "Switch" — a verb where a description goes, which made the row parse as a
  // person called "Ming Switch".
  assert.equal(
    document.byId.get("local-account-hint").textContent,
    "This machine only",
    "the subtitle states what this identity is connected to, never what to do to it",
  );
  assert.equal(
    document.byId.get("settings-overlay").hidden,
    true,
    "nothing about the identity is on screen until it is asked for",
  );

  document.byId.get("local-account-row").click();
  await settle();
  assert.match(
    document.byId.get("local-account-binding-state").textContent,
    /No WorkMax account connected/i,
    "the section states the binding, not a login state",
  );
  assert.equal(
    document.byId.get("local-account-connect").hidden,
    false,
    "connecting is offered as an action ON this identity",
  );
  assert.equal(
    document.byId.get("local-account-disconnect").hidden,
    true,
    "there is nothing to disconnect from yet",
  );
  const switcherNames = walk(
    document.byId.get("local-account-list"),
    (node) => node.classList?.contains("local-account-item"),
  ).map((node) => node.textContent);
  assert.equal(switcherNames.length, 2, "both local identities are switchable without any model");
  assert.equal(
    document.byId.get("settings-panel-account").hidden,
    false,
    "the row opens the section its contents live in, not a floating copy of them",
  );
}

// Connected: the same row, now stating what it is bound to — and offering the
// one action that changes it. Switching local identities is NOT offered,
// because while an account is connected new work belongs to that account and
// a switch would change nothing.
async function testConnectedAccountIsShownAsABindingOnTheLocalIdentity() {
  const accounts = [{ id: 1, name: "Ming", active: true }];
  const { bridge, desktopBridge } = localModeBridge({
    localRoute: false,
    accounts,
    binding: { state: "bound", user_id: "…42" },
  });
  let logoutCalls = 0;
  bridge.fetch = async (pathname) => {
    if (pathname === "/auth/status") {
      return response({
        state: logoutCalls === 0 ? "authenticated" : "unauthenticated",
        tier: "pro",
        updated_at: "2026-08-08T00:00:00Z",
      });
    }
    if (pathname === "/agent/threads?include_paused=true") return response({ items: [] });
    if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
    throw new Error(`unexpected fetch path ${pathname}`);
  };
  desktopBridge.agent.listSkills = async () =>
    typedSuccess({ items: [], count: 0, allowed_modes: ["ppt"] });
  desktopBridge.auth = {
    async beginLogin() { return { state: "awaiting_password" }; },
    async loginStatus() { return { state: "idle" }; },
    async submitLoginPassword() { throw new Error("not exercised"); },
    async cancelLogin() { return { state: "idle" }; },
    async logout() {
      logoutCalls += 1;
      // The sidecar's logout, unchanged: the binding goes away, the local
      // identity does not.
      desktopBridge.local.listAccounts = async () =>
        typedSuccess({ items: accounts.slice(), count: accounts.length, binding: { state: "unbound" } });
      return typedSuccess({ ok: true, revoke_status: "ok" });
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();
  assert.equal(
    document.byId.get("local-account-row").hidden,
    false,
    "a connected account does not erase who you are on this machine",
  );
  assert.equal(
    document.byId.get("local-account-hint").textContent,
    "Connected to WorkMax",
    "bound is a state of this identity, and the row is where that state is stated",
  );
  document.byId.get("local-account-row").click();
  await settle();
  assert.match(
    document.byId.get("local-account-binding-state").textContent,
    /Connected to a WorkMax account \(…42\)/,
    "the panel names the connected account, masked",
  );
  assert.equal(document.byId.get("local-account-connect").hidden, true);
  assert.equal(document.byId.get("local-account-disconnect").hidden, false);
  assert.equal(
    document.byId.get("local-account-list").hidden,
    true,
    "switching local identities while bound would change nothing — do not offer it",
  );

  document.byId.get("local-account-disconnect").click();
  await settle();
  await settle();
  await settle();
  assert.equal(logoutCalls, 1, "Disconnect is the existing logout");
  assert.equal(
    document.byId.get("chat-input").disabled,
    true,
    "with the account gone and no local model, a prompt has nowhere to run",
  );
  assert.equal(
    document.byId.get("local-account-row").hidden,
    false,
    "and the machine's own identity is still right there",
  );
  assert.match(
    document.byId.get("status-card").textContent,
    /Working as Ming on this machine/,
    "the app lands on the local identity rather than a sign-in wall",
  );
}

async function testSignedOutLocalRouteCanDriveTheAgent() {
  const { bridge, desktopBridge } = localModeBridge({ localRoute: true });
  // A login bridge that answers "nobody is signing in", which is the shape a
  // real signed-out boot has. Without it restoreLoginTransaction reports a
  // missing service and the status strip below would be measuring the fixture.
  desktopBridge.auth = {
    async beginLogin() { return { state: "awaiting_password" }; },
    async loginStatus() { return { state: "idle" }; },
    async submitLoginPassword() { throw new Error("not exercised"); },
    async cancelLogin() { return { state: "idle" }; },
  };
  // What a source build really reports: the shim spells the absence of a
  // version stamp "unknown", which is truthy, which put "sidecar unknown ·
  // app unknown" at the foot of the rail on every dev build — the exact
  // sentence the code above it says it exists to avoid.
  bridge.sidecarVersion = "unknown";
  bridge.appVersion = "unknown";
  const { document, ns } = await runRenderer(bridge, desktopBridge);

  assert.match(
    document.byId.get("thread-list").textContent,
    /Offline notes/,
    "local threads must load without a cloud session; they belong to the local single user",
  );
  assert.equal(
    document.byId.get("local-account-connect").hidden,
    false,
    "signing in must still be offered — local mode is a way to work, not a replacement",
  );
  // Where the turn runs is ambient state, so it moved to the identity row's
  // subtitle (asserted with an identity present, below). What it left behind
  // matters just as much: the status strip used to open with a permanent
  // "Local model route. No account connected — history stays on this machine.",
  // reserving a paragraph of the rail for a fact that never changes while you
  // work — and teaching the reader to stop looking at the one place errors
  // appear.
  assert.equal(
    document.byId.get("status-card").textContent,
    "",
    "nothing permanent may sit on the status strip: it is for what just happened",
  );
  assert.equal(
    document.byId.get("status-bar").hidden,
    true,
    "an empty strip takes no space: setStatus hides the bar when the line is empty",
  );
  assert.equal(
    document.byId.get("runtime-label").hidden,
    true,
    'an unstamped build must say nothing, not "sidecar unknown · app unknown"',
  );

  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button"),
  )[0];
  threadButton.click();
  await settle();

  assert.equal(
    document.byId.get("chat-input").disabled,
    false,
    "the composer must be usable: this is exactly the configuration the local route exists for",
  );
  assert.match(
    document.byId.get("composer-status").textContent,
    /local model, chat only/i,
    "the composer must say where the turn runs AND that no tools are wired — the dispatch falls back silently, the composer must not",
  );

  // And a turn actually goes out.
  const input = document.byId.get("chat-input");
  input.value = "What did I write yesterday?";
  input.dispatch("input");
  assert.equal(document.byId.get("send-button").disabled, false);
}

// The other half of the rule, and the change this whole pass is about: with
// no account AND no local model, the WORKBENCH is still yours — history,
// drafts, settings, identities — and only SENDING is out of reach. The app
// used to answer this state by hiding everything behind a sign-in wall.
async function testSignedOutWithoutAModelKeepsTheWorkbench() {
  const accounts = [{ id: 1, name: "Ming", active: true }];
  const { bridge, desktopBridge } = localModeBridge({ localRoute: false, accounts });
  const { document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();

  assert.match(
    document.byId.get("thread-list").textContent,
    /Offline notes/,
    "the machine's own history belongs to the machine's own identity — show it",
  );
  assert.match(
    document.byId.get("empty-title").textContent,
    /You're working as Ming/,
    "the first-run question is where the model runs, not who you are",
  );
  assert.equal(
    document.byId.get("onboarding-paths").hidden,
    false,
    "both ways of getting a model are presented side by side",
  );
  assert.equal(
    document.byId.get("empty-new-thread-button").disabled,
    false,
    "starting a conversation is workbench work: the sidecar creates it locally",
  );
  assert.equal(
    document.byId.get("starter-prompts").hidden,
    true,
    "the starter cards promise a turn — do not offer them before there is a model to run one",
  );

  // The form opens AND submits: one predicate, no model required.
  document.byId.get("empty-new-thread-button").click();
  await settle();
  assert.equal(document.byId.get("new-thread-form").hidden, false);
  const nameInput = document.byId.get("new-thread-name");
  nameInput.value = "Notes before I decide";
  nameInput.dispatch("input");
  assert.equal(
    document.byId.get("new-thread-submit-button").disabled,
    false,
    "a conversation can be started before choosing where the model runs",
  );
  document.byId.get("new-thread-cancel-button").click();
  await settle();

  // Opening a conversation works. Sending does not, and says why.
  walk(document.byId.get("thread-list"), (node) => node.classList?.contains("thread-button"))[0].click();
  await settle();
  assert.equal(document.byId.get("thread-panel").hidden, false, "the conversation opens");
  assert.equal(document.byId.get("chat-input").disabled, true);
  assert.equal(document.byId.get("send-button").disabled, true);
  assert.match(
    document.byId.get("composer-status").textContent,
    /connect a WorkMax account or set a local model/i,
    "the composer names both ways forward instead of saying 'sign in'",
  );
}

// Markdown is what models write, so it is what the chat column has to read
// back. This drives the real path: a turn streams Markdown, finishes, and the
// bubble is asserted structurally — elements, not a string that happens to
// contain the right characters.
async function testAssistantMarkdownIsRenderedAsElements() {
  let emit = null;
  const answer = [
    "## Findings",
    "",
    "Revenue is **up** and costs are `flat`.",
    "",
    "- first point",
    "- second point",
    "",
    "```sql",
    "SELECT 1;",
    "```",
    "",
    "See [the plan](https://example.com/plan) or [this](javascript:alert(1)).",
    "",
    "> quoted remark",
    "",
    "Not a tag: <img src=x onerror=alert(1)>",
  ].join("\n");

  // The turn is reconciled against the sidecar once it completes, so the
  // bubble that ends up on screen is the one rendered from cached history —
  // which is the path a reopened thread takes too. Serving the answer back
  // here means this test covers both.
  let answered = false;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("md-thread", "Markdown")] });
      }
      if (pathname === "/agent/threads/md-thread/messages") {
        return response({
          items: answered
            ? [{
                uuid: "md-msg",
                user_text: "Summarise",
                ai_text: answer,
                streaming_state: "complete",
                created_at: "2026-05-21T00:00:00Z",
                updated_at: "2026-05-21T00:00:00Z",
              }]
            : [],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        emit = (event) => callback({ ...event, turnID: "md-turn" });
        return { turnID: "md-turn" };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  const input = document.byId.get("chat-input");
  input.value = "Summarise";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  emit({ type: "text_delta", delta: answer });
  await settle();

  const streaming = walk(
    document.byId.get("message-list"),
    (n) => n.classList?.contains("bubble") && n.parentNode?.classList?.contains("assistant"),
  ).at(-1);
  // Streaming formatting is incremental: blocks whose closing blank line has
  // arrived are already typeset, while the still-ambiguous tail stays raw
  // text. The final line of the fixture has no newline yet, so it must be
  // sitting untouched in the tail — not parsed early into a shape that could
  // change as the rest of it arrives.
  assert.equal(
    streaming.classList.contains("markdown"),
    true,
    "completed blocks must be typeset while the answer still streams",
  );
  {
    const tail = walk(streaming, (n) => n.classList?.contains("md-stream-tail"));
    assert.equal(tail.length, 1, "the unfinished remainder must live in the raw tail");
    assert.match(
      tail[0].textContent,
      /Not a tag: <img src=x onerror=alert\(1\)>/,
      "the incomplete final line stays raw text until its block closes",
    );
    assert.equal(
      walk(streaming, (n) => n.tagName === "IMG").length,
      0,
      "markup in model output must stay text during the stream too",
    );
  }

  answered = true;
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();

  const bubble = walk(
    document.byId.get("message-list"),
    (n) => n.classList?.contains("bubble") && n.parentNode?.classList?.contains("assistant"),
  ).at(-1);
  assert.ok(bubble, "the reconciled thread must still show the assistant's answer");
  assert.equal(bubble.classList.contains("markdown"), true, "the finished answer must be formatted");

  const tags = (name) => walk(bubble, (n) => n.tagName === name);
  // Heading levels are clamped so model output cannot outrank the app's own.
  assert.equal(tags("H5").length, 1, "'##' must become a heading, clamped below the app's headings");
  assert.equal(tags("H1").length + tags("H2").length + tags("H3").length, 0);
  assert.equal(tags("STRONG").length, 1, "**up** must be emphasis, not literal asterisks");
  assert.equal(tags("UL").length, 1);
  assert.equal(tags("LI").length, 2);
  assert.equal(tags("BLOCKQUOTE").length, 1);

  const pre = tags("PRE");
  assert.equal(pre.length, 1, "a fenced block must become a code block");
  assert.equal(pre[0].textContent, "SELECT 1;", "the code must be the code, without the fence");
  assert.equal(walk(pre[0], (n) => n.tagName === "CODE")[0].className, "language-sql");
  const lang = walk(bubble, (n) => n.classList?.contains("md-code-lang"));
  assert.equal(lang.length, 1, "the fence's language must be named, not only classed");
  assert.equal(lang[0].textContent, "sql");
  assert.doesNotMatch(pre[0].textContent, /sql.*SELECT/su,
    "the label must live outside the block's selectable text");

  const links = tags("A");
  assert.equal(links.length, 1, "only the http link may become an anchor");
  assert.equal(links[0].getAttribute("href"), "https://example.com/plan");
  assert.match(
    bubble.textContent,
    /\[this\]\(javascript:alert\(1\)\)/,
    "a javascript: link must be shown as the literal text the model wrote, not offered as a link",
  );

  // The security property, stated as a test: markup in model output is text.
  assert.equal(tags("IMG").length, 0, "model output must never become an element");
  assert.match(
    bubble.textContent,
    /<img src=x onerror=alert\(1\)>/,
    "the tag must be visible to the user as the characters it is",
  );

  // And the user's own words are shown back unchanged.
  const userBubble = walk(
    document.byId.get("message-list"),
    (n) => n.classList?.contains("bubble") && n.parentNode?.classList?.contains("user"),
  ).at(-1);
  assert.equal(userBubble.classList.contains("markdown"), false);
}

// The retrieval announcement is the only way the user learns that an answer
// came out of their own documents rather than out of the model. This drives it
// the whole way: a turn streams the event, the panel names the sources, and
// the next turn does not inherit them.
async function testRetrievedContextIsShownAndResetPerTurn() {
  let emit = null;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("rag-thread", "Grounded answers")] });
      }
      if (pathname === "/agent/threads/rag-thread/messages") return response({ items: [] });
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  let turnSeq = 0;
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      startTurn(input, callback) {
        turnSeq += 1;
        const turnID = `rag-turn-${turnSeq}`;
        emit = (event) => callback({ ...event, turnID });
        return { turnID };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  assert.equal(
    document.byId.get("context-retrieved").hidden,
    true,
    "with nothing retrieved the whole section stands down — an empty module explaining itself costs more than it returns",
  );

  const input = document.byId.get("chat-input");
  input.value = "How did Q3 go?";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  emit({
    type: "retrieval",
    sources: [
      { kind: "file", label: "q3-plan.md", snippet: "Revenue grew 12%.", score: 0.88 },
      { kind: "conversation", label: "Earlier conversation", snippet: "We set the Q3 target.", score: 0.51 },
    ],
  });
  emit({ type: "text_delta", delta: "Revenue was up." });
  await settle();

  assert.equal(document.byId.get("retrieved-meta").textContent, "2 passages");
  assert.equal(
    document.byId.get("context-retrieved").hidden,
    false,
    "the section appears the moment there is something to attribute",
  );
  const listed = document.byId.get("retrieved-list");
  assert.equal(listed.children.length, 2);
  assert.match(listed.textContent, /q3-plan\.md/, "the file must be named");
  assert.match(
    listed.textContent,
    /Revenue grew 12%\./,
    "the passage handed to the model must be shown verbatim, not summarised",
  );
  assert.match(listed.textContent, /88% match/, "the score must be rendered as a similarity");

  // A new question invalidates the old provenance. Nothing clears it on the
  // way in — a turn that retrieves nothing sends no event at all — so if this
  // is not cleared at turn start the panel credits the new answer to the old
  // documents.
  emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
  await settle();
  input.value = "Unrelated question";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  assert.equal(
    document.byId.get("retrieved-list").children.length,
    0,
    "the previous turn's sources must not survive into the next turn",
  );
  assert.equal(
    document.byId.get("context-retrieved").hidden,
    true,
    "and the section stands back down with them",
  );
}

// runShimTurn drives the shipped shim over a canned SSE body: real frame
// parsing, real validation, real callbacks. The shim is an IIFE, so nothing
// inside it can be poked at directly — which is the right constraint. What
// matters is what a turn delivers, and that is observable from here.
async function runShimTurn(sseText) {
  const shimPath = path.join(rendererDir, "shim.js");
  const shimSource = fs.readFileSync(shimPath, "utf8");
  const encoded = new TextEncoder().encode(sseText);
  let sent = false;

  const context = {
    console,
    URL,
    Headers,
    TextEncoder,
    TextDecoder,
    AbortController,
    crypto: { randomUUID: () => "00000000-0000-4000-8000-00000000beef" },
    fetch: async () => ({
      ok: true,
      status: 200,
      statusText: "OK",
      headers: new Map([["content-type", "text/event-stream"]]),
      body: {
        getReader: () => ({
          async read() {
            if (sent) return { done: true, value: undefined };
            sent = true;
            return { done: false, value: encoded };
          },
          async cancel() {},
        }),
      },
    }),
    setTimeout, clearTimeout,
    location: { origin: "http://127.0.0.1:5000" },
    document: {
      baseURI: "http://127.0.0.1:5000/CAPABILITY/",
      documentElement: { dataset: {} },
      addEventListener() {},
    },
  };
  context.window = context;
  // The shim hands its turn functions to the generated bridge factory rather
  // than putting them on a global, so the factory is where they are reachable
  // from. Capturing them is not a back door: it is the same object the real
  // lib/desktop-bridge.js receives.
  let transport = null;
  context.window.__workmaxDesktopBridge = {
    createDesktopBridge: (deps) => {
      transport = deps;
      return {};
    },
  };
  vm.createContext(context);
  vm.runInContext(shimSource, context, { filename: shimPath });
  assert.ok(transport, "shim.js must build the typed bridge");

  const events = [];
  transport.startAgentTurn({ thread_uuid: "t" }, (event) => events.push(event));
  await settle();
  return events;
}

const SSE_DONE_FRAME = 'event: done\ndata: {"type":"done","result":"OK"}\n\n';

function retrievalFrame(payload) {
  return `event: retrieval\ndata: ${JSON.stringify(payload)}\n\n`;
}

// The shim decides whether a payload from the sidecar is allowed to reach the
// renderer at all. These are the shapes it must refuse — and, just as
// important, the fact that refusing one must not cost the user the answer.
async function testShimValidatesRetrievalPayloads() {
  const good = await runShimTurn(
    retrievalFrame({
      sources: [
        { kind: "file", label: "a.md", snippet: "text", score: 0.5 },
        { kind: "conversation", label: "Earlier conversation", snippet: "prior", score: 0.25 },
      ],
    }) + SSE_DONE_FRAME,
  );
  const retrieval = good.find((e) => e.type === "retrieval");
  assert.ok(retrieval, "a well-formed retrieval frame must be delivered");
  assert.equal(retrieval.sources.length, 2);
  assert.equal(retrieval.sources[0].label, "a.md");
  assert.equal(retrieval.sources[0].score, 0.5);
  const doneEvent = good.find((e) => e.type === "done");
  assert.ok(doneEvent, "the turn must still complete");
  // The shape the renderer's parser demands, from the sidecar's actual done
  // frame. This exact seam shipped broken — the shim emitted no result and
  // the renderer refused every real completion; every VM fixture called the
  // callback directly and skipped the dispatch, so only a click against the
  // live app could see it. The shim's SSE-driven output now goes through the
  // renderer's own validation here.
  assert.deepEqual(
    { code: doneEvent.result.code, subtype: doneEvent.result.subtype, is_error: doneEvent.result.is_error },
    { code: "", subtype: "", is_error: false },
    "the done frame must normalize into the typed result the renderer parses",
  );

  // Each of these is malformed in a different way. All must be dropped, and
  // none may turn into a protocol error: the provenance list is informational,
  // and failing a delivered answer over it would be a bad trade.
  const refused = [
    { sources: "not-an-array" },
    { sources: [{ kind: "file", label: "" }] },
    { sources: [{ kind: "deliverable", label: "a.md" }] },
    { sources: [{ label: "a.md" }] },
  ];
  for (const payload of refused) {
    const events = await runShimTurn(retrievalFrame(payload) + SSE_DONE_FRAME);
    assert.equal(
      events.some((e) => e.type === "retrieval"),
      false,
      `malformed payload must not be delivered: ${JSON.stringify(payload)}`,
    );
    assert.equal(
      events.some((e) => e.type === "protocol_error"),
      false,
      `a malformed provenance list must not fail the turn: ${JSON.stringify(payload)}`,
    );
    assert.ok(events.some((e) => e.type === "done"), "the answer must still arrive");
  }

  // A score outside 0..1 is clamped rather than refused: the source really was
  // used, and losing that over a rounding artefact would be the worse error.
  const clamped = await runShimTurn(
    retrievalFrame({ sources: [
      { kind: "file", label: "high.md", score: 4 },
      { kind: "file", label: "low.md", score: -1 },
      { kind: "file", label: "text.md", score: "high" },
    ] }) + SSE_DONE_FRAME,
  ).then((events) => events.find((e) => e.type === "retrieval"));
  assert.equal(clamped.sources[0].score, 1);
  assert.equal(clamped.sources[1].score, 0);
  assert.equal(
    clamped.sources[2].score,
    null,
    "a non-numeric score becomes absent, not zero — zero would read as 'no match'",
  );

  const bounded = await runShimTurn(
    retrievalFrame({
      sources: Array.from({ length: 40 }, () => ({
        kind: "file",
        label: "x".repeat(500),
        snippet: "y".repeat(5000),
      })),
    }) + SSE_DONE_FRAME,
  ).then((events) => events.find((e) => e.type === "retrieval"));
  assert.equal(bounded.sources.length, 12, "the list must be bounded whatever the sidecar sends");
  assert.equal(bounded.sources[0].label.length, 120);
  assert.equal(bounded.sources[0].snippet.length, 400);
}

// The two L2 frames the sidecar grew for the approval loop. Well-formed ones
// must arrive; malformed ones must be dropped without costing the answer — an
// approval card without an id could only ever fail, and reasoning is
// narration, never worth the turn.
async function testShimDropsMalformedApprovalAndReasoningFrames() {
  const good = await runShimTurn(
    'event: approval_request\ndata: {"id":"ap-1","name":"Write","target":"a.md"}\n\n' +
    'event: approval_request\ndata: {"id":"ap-2","name":"Bash"}\n\n' +
    'event: reasoning_delta\ndata: {"delta":"thinking hard"}\n\n' +
    SSE_DONE_FRAME,
  );
  const approval = good.find((e) => e.type === "approval_request");
  assert.ok(approval, "a well-formed approval frame must be delivered");
  assert.equal(approval.id, "ap-1");
  assert.equal(approval.name, "Write");
  assert.equal(approval.target, "a.md");
  const bare = good.filter((e) => e.type === "approval_request")[1];
  assert.equal(bare.target, "", "an absent target normalizes to empty, like tool_use");
  const reasoning = good.find((e) => e.type === "reasoning_delta");
  assert.ok(reasoning, "a well-formed reasoning frame must be delivered");
  assert.equal(reasoning.delta, "thinking hard");
  assert.ok(good.some((e) => e.type === "done"));

  // turn_meta: which engine ran the turn, and which model it was told to use.
  const meta = await runShimTurn(
    'event: turn_meta\ndata: {"engine":"pi","model":"qwen3-coder"}\n\n' + SSE_DONE_FRAME,
  );
  const provenance = meta.find((e) => e.type === "turn_meta");
  assert.ok(provenance, "a well-formed turn_meta frame must be delivered");
  assert.equal(provenance.engine, "pi");
  assert.equal(provenance.model, "qwen3-coder");
  assert.equal(provenance.mind, "", "an absent mind normalizes to empty, not to a guess");
  const withMind = await runShimTurn(
    'event: turn_meta\ndata: {"engine":"pi","model":"m","mind":"Payroll mind"}\n\n' + SSE_DONE_FRAME,
  );
  assert.equal(withMind.find((e) => e.type === "turn_meta").mind, "Payroll mind");
  const longMind = await runShimTurn(
    `event: turn_meta\ndata: {"engine":"pi","mind":"${"n".repeat(200)}"}\n\n` + SSE_DONE_FRAME,
  );
  assert.equal(longMind.find((e) => e.type === "turn_meta").mind.length, 64);
  // An engine may pick its own default. The absent model normalizes to empty
  // rather than being invented, and the frame still arrives — knowing WHICH
  // engine answered is most of the value.
  const bareMeta = await runShimTurn(
    'event: turn_meta\ndata: {"engine":"claude"}\n\n' + SSE_DONE_FRAME,
  );
  const bareProvenance = bareMeta.find((e) => e.type === "turn_meta");
  assert.ok(bareProvenance, "a turn_meta without a model is still a fact");
  assert.equal(bareProvenance.model, "");
  for (const payload of [
    '{"model":"m"}',
    '{"engine":"","model":"m"}',
    '{"engine":42}',
    `{"engine":"${"e".repeat(33)}"}`,
    "not json",
  ]) {
    const events = await runShimTurn(
      `event: turn_meta\ndata: ${payload}\n\n` + SSE_DONE_FRAME,
    );
    assert.equal(
      events.some((e) => e.type === "turn_meta"),
      false,
      `a turn_meta with no usable engine must be dropped: ${payload}`,
    );
    assert.ok(
      events.some((e) => e.type === "done"),
      "and dropping provenance must never cost the answer",
    );
  }
  // The model is user-typed configuration, so it is bounded rather than
  // refused: an over-long id costs its tail, not the provenance line.
  const longModel = await runShimTurn(
    `event: turn_meta\ndata: {"engine":"pi","model":"${"m".repeat(200)}"}\n\n` + SSE_DONE_FRAME,
  );
  assert.equal(longModel.find((e) => e.type === "turn_meta").model.length, 80);

  const refused = [
    '{"name":"Write","target":"a.md"}',
    '{"id":"ap-1","target":"a.md"}',
    '{"id":"","name":"Write"}',
    '{"id":42,"name":"Write"}',
    `{"id":"${"x".repeat(33)}","name":"Write"}`,
    `{"id":"ap-1","name":"${"n".repeat(65)}"}`,
    "not json",
  ];
  for (const payload of refused) {
    const events = await runShimTurn(
      `event: approval_request\ndata: ${payload}\n\n` + SSE_DONE_FRAME,
    );
    assert.equal(
      events.some((e) => e.type === "approval_request"),
      false,
      `a frame that cannot be answered must be dropped: ${payload}`,
    );
    assert.equal(
      events.some((e) => e.type === "protocol_error"),
      false,
      `and must not fail the turn: ${payload}`,
    );
    assert.ok(events.some((e) => e.type === "done"), "the answer must still arrive");
  }

  // The target is clipped like tool_use's, not refused: the card is still
  // answerable, only its label is bounded.
  const clipped = await runShimTurn(
    `event: approval_request\ndata: {"id":"ap-9","name":"Write","target":"${"t".repeat(120)}"}\n\n` +
    SSE_DONE_FRAME,
  ).then((events) => events.find((e) => e.type === "approval_request"));
  assert.equal(clipped.target.length, 80);

  // Reasoning past the renderer's per-event bound is dropped, not fatal.
  const oversized = await runShimTurn(
    `event: reasoning_delta\ndata: {"delta":"${"r".repeat(262145)}"}\n\n` + SSE_DONE_FRAME,
  );
  assert.equal(oversized.some((e) => e.type === "reasoning_delta"), false);
  assert.equal(oversized.some((e) => e.type === "protocol_error"), false);
  assert.ok(oversized.some((e) => e.type === "done"));
  const nonString = await runShimTurn(
    'event: reasoning_delta\ndata: {"delta":42}\n\n' + SSE_DONE_FRAME,
  );
  assert.equal(nonString.some((e) => e.type === "reasoning_delta"), false);
  assert.ok(nonString.some((e) => e.type === "done"));
}

async function testStagedAttachmentsAreSentWithTheTurn() {
  let sentFileIDs = null;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("attach-thread", "Attachments")] });
      }
      if (pathname === "/agent/threads/attach-thread/messages") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        return typedSuccess({ file_id: 4242 });
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      startTurn(input, callback) {
        sentFileIDs = input.fileIDs;
        callback({ type: "text_delta", turnID: "attach-turn", delta: "ok" });
        callback({
          type: "done",
          turnID: "attach-turn",
          result: { code: "", subtype: "", is_error: false },
        });
        return { turnID: "attach-turn" };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { document, context, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  // Stage one uploaded attachment the way the upload callback does.
  // uploadThreadFile is a top-level renderer function; the VM context exposes
  // it, and a plain object is enough because the renderer only reads name/size
  // before handing the file to the bridge.
  ns.uploadThreadFile({ name: "notes.txt", size: 12 });
  await settle();

  const input = document.byId.get("chat-input");
  input.value = "Summarise the attachment";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  // Array.from because the value crossed a VM realm boundary: same contents,
  // different Array prototype, and deepStrictEqual cares.
  assert.deepEqual(
    Array.from(sentFileIDs ?? []),
    [4242],
    "the staged attachment id must reach startTurn; an empty list means the tray was cleared first"
  );
  assert.equal(
    document.byId.get("attachment-chips").hidden,
    true,
    "the tray must be cleared once the turn owns the ids"
  );
}

async function testSynchronousTurnCallbacksAreBufferedUntilOpenResult() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("sync-callback-thread", "Synchronous callback")] });
      }
      if (pathname === "/agent/threads/sync-callback-thread/messages") {
        return response({
          items: [message("sync-callback-message", "Synchronous prompt", "Cached sync answer")],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      startTurn(_input, callback) {
        callback({
          type: "unknown",
          turnID: "sync-callback-turn",
          event: "private_sync_payload",
        });
        callback({
          type: "text_delta",
          turnID: "sync-callback-turn",
          delta: "Buffered live answer",
        });
        callback({
          type: "done",
          turnID: "sync-callback-turn",
          result: { code: "", subtype: "", is_error: false },
        });
        return { turnID: "sync-callback-turn" };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Synchronous prompt";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  assert.match(document.byId.get("message-list").textContent, /Buffered live answer/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /sync-callback-secret/);
  assert.match(document.byId.get("turn-state").textContent, /^Done · \d+s$/);
  await settle();
  assert.match(document.byId.get("message-list").textContent, /Cached sync answer/);
}

async function testAgentTurnStreamsAndReconciles() {
  const fetchCalls = [];
  const startCalls = [];
  let streamCallback;
  let messageReads = 0;
  let threadReads = 0;
  let skillReads = 0;
  const bridge = {
    sidecarVersion: "sidecar-agent",
    appVersion: "app-agent",
    async fetch(pathname) {
      fetchCalls.push(pathname);
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", tier: "pro", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        threadReads += 1;
        return response({
          items: [thread("thread-agent", "Deck draft", threadReads === 1 ? 1 : 2)],
        });
      }
      if (pathname === "/agent/threads/thread-agent/messages") {
        messageReads += 1;
        return response({
          items:
            messageReads === 1
              ? [message("message-initial", "Initial prompt", "Initial answer")]
              : [
                  message("message-initial", "Initial prompt", "Initial answer"),
                  message("message-final", "Refine the deck", "Final cached answer"),
                ],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        skillReads += 1;
        return typedSuccess(pptCatalog());
      },
      startTurn(input, callback) {
        startCalls.push({
          threadUUID: input.threadUUID,
          userText: input.userText,
          chatMode: input.chatMode,
        });
        streamCallback = callback;
        return { turnID: "turn-agent" };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  assert.equal(skillReads, 1);
  assert.equal(
    document.byId.get("new-thread-button").disabled,
    true,
    "an alpha.4-style Agent bridge must keep existing-thread chat while New stays unavailable"
  );
  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button")
  )[0];
  threadButton.click();
  await settle();

  assert.equal(document.byId.get("agent-mode").value, "ppt");
  assert.equal(
    document.byId.get("agent-mode").hidden,
    true,
    "one allowed skill is not a choice; the selector must stand down until there are two",
  );
  assert.equal(document.byId.get("chat-input").disabled, false);
  const chatInput = document.byId.get("chat-input");
  chatInput.value = "  Refine the deck  ";
  chatInput.dispatch("input");
  assert.equal(document.byId.get("send-button").disabled, false);

  let prevented = 0;
  chatInput.dispatch("keydown", {
    key: "Enter",
    metaKey: true,
    preventDefault() {
      prevented += 1;
    },
  });
  document.byId.get("chat-form").submit();
  assert.equal(prevented, 1);
  assert.equal(startCalls.length, 1, "a second submit while streaming must be ignored");
  assert.deepEqual(startCalls[0], {
    threadUUID: "thread-agent",
    userText: "Refine the deck",
    chatMode: "ppt",
  });
  assert.match(document.byId.get("message-list").textContent, /Refine the deck/);
  assert.equal(document.byId.get("stop-button").hidden, false);

  // Before the first token, the empty assistant bubble must wear the typing
  // indicator — a silent wait reads as broken.
  const pendingNow = walk(
    document.byId.get("message-list"),
    (n) => n.classList?.contains("assistant") && n.classList?.contains("pending"),
  );
  assert.equal(pendingNow.length, 1, "the streamed answer must show a typing indicator while empty");

  streamCallback({
    type: "unknown",
    turnID: "turn-agent",
    event: "private_tool_payload",
  });
  assert.doesNotMatch(document.byId.get("message-list").textContent, /unknown-event-secret/);
  assert.doesNotMatch(
    ns.state.activeTurn.assistantText,
    /unknown-event-secret/
  );
  assert.equal(ns.state.activeTurn.pendingEvents.length, 0);

  streamCallback({ type: "text_delta", turnID: "turn-agent", delta: "Live " });
  assert.equal(
    walk(
      document.byId.get("message-list"),
      (n) => n.classList?.contains("assistant") && n.classList?.contains("pending"),
    ).length,
    0,
    "the first token must retire the typing indicator",
  );
  streamCallback({ type: "text_delta", turnID: "turn-agent", delta: "answer" });
  // Deltas paint on the next frame, not per event — settle lets it flush.
  await settle();
  assert.match(document.byId.get("message-list").textContent, /Live answer/);
  assert.equal(document.byId.get("turn-state").textContent, "Working");

  streamCallback({
    type: "done",
    turnID: "turn-agent",
    result: { code: "OK", subtype: "already_processed", is_error: false },
  });
  await settle();
  await settle();
  assert.match(document.byId.get("turn-state").textContent, /^Done · \d+s$/);
  assert.equal(document.byId.get("stop-button").hidden, true);
  assert.equal(document.byId.get("chat-input").disabled, false);
  assert.equal(messageReads, 2, "done must reconcile the cached message history");
  assert.equal(threadReads, 2, "done must reconcile thread metadata");
  assert.match(document.byId.get("message-list").textContent, /Final cached answer/);
  assert.equal(fetchCalls.filter((path) => path.endsWith("/messages")).length, 2);
}

async function testLateThreadHistoryCannotContaminateSelection() {
  let resolveThreadA;
  const pendingThreadA = new Promise((resolve) => {
    resolveThreadA = resolve;
  });
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          items: [thread("thread-a", "Thread A"), thread("thread-b", "Thread B")],
        });
      }
      if (pathname === "/agent/threads/thread-a/messages") {
        return pendingThreadA;
      }
      if (pathname === "/agent/threads/thread-b/messages") {
        return response({ items: [message("message-b", "Prompt B", "Answer B")] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };

  const { document, ns } = await runRenderer(bridge);
  const buttons = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button")
  );
  buttons[0].click();
  buttons[1].click();
  await settle();
  assert.match(document.byId.get("message-list").textContent, /Answer B/);

  resolveThreadA(
    response({ items: [message("message-a", "Prompt A", "Late answer A")] })
  );
  await settle();
  assert.match(document.byId.get("message-list").textContent, /Answer B/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /Late answer A/);
  assert.equal(document.byId.get("thread-title").textContent, "Thread B");
}

async function testThreadSwitchCancelsAndFencesOldTurn() {
  let oldTurnCallback;
  const canceled = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          items: [thread("turn-thread-a", "Turn A"), thread("turn-thread-b", "Turn B")],
        });
      }
      if (pathname === "/agent/threads/turn-thread-a/messages") {
        return response({ items: [message("turn-message-a", "A", "Cached A")] });
      }
      if (pathname === "/agent/threads/turn-thread-b/messages") {
        return response({ items: [message("turn-message-b", "B", "Cached B")] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      startTurn(_input, callback) {
        oldTurnCallback = callback;
        return { turnID: "old-turn" };
      },
      async cancelTurn(turnID) {
        canceled.push(turnID);
        return { turnID, canceled: true };
      },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  const buttons = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button")
  );
  buttons[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Continue A";
  input.dispatch("input");
  input.dispatch("keydown", { key: "Enter", ctrlKey: true });
  buttons[1].click();
  await settle();

  assert.deepEqual(canceled, ["old-turn"]);
  assert.match(document.byId.get("message-list").textContent, /Cached B/);
  oldTurnCallback({
    type: "text_delta",
    turnID: "old-turn",
    delta: "late-old-turn-secret",
  });
  oldTurnCallback({
    type: "done",
    turnID: "old-turn",
    result: { code: "", subtype: "", is_error: false },
  });
  await settle();
  assert.doesNotMatch(document.byId.get("message-list").textContent, /late-old-turn-secret/);
  assert.match(document.byId.get("message-list").textContent, /Cached B/);
  assert.equal(document.byId.get("thread-title").textContent, "Turn B");
}

async function testStopTurnIsSingleShot() {
  let cancelCalls = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("stop-thread", "Stop thread")] });
      }
      if (pathname === "/agent/threads/stop-thread/messages") {
        return response({ items: [message("stop-message", "Before", "Cached") ] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      startTurn() {
        return { turnID: "stop-turn" };
      },
      async cancelTurn(turnID) {
        cancelCalls += 1;
        return { turnID, canceled: true };
      },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Stop this generation";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  const stop = document.byId.get("stop-button");
  stop.click();
  stop.click();
  await settle();
  assert.equal(cancelCalls, 1);
  assert.match(document.byId.get("turn-state").textContent, /^Stopped · \d+s$/);
  assert.equal(stop.hidden, true);
  assert.equal(document.byId.get("chat-input").disabled, false);
}

async function testInitialTurnBusyRefreshesRecoveryWithoutReplay() {
  const turnID = "123e4567-e89b-42d3-a456-426614174011";
  const interrupted = recoverableTurn({
    turn_uuid: turnID,
    thread_uuid: "initial-busy-thread",
    user_text: "Initial busy prompt",
    last_error_kind: "turn_in_progress",
  });
  let streamCallback;
  let startCalls = 0;
  let resumeCalls = 0;
  let recoveryReads = 0;
  let messageReads = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("initial-busy-thread", "Initial busy")] });
      }
      if (pathname === "/agent/threads/initial-busy-thread/messages") {
        messageReads += 1;
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        recoveryReads += 1;
        return typedSuccess({ items: [], count: 0 });
      },
      startTurn(_input, callback) {
        startCalls += 1;
        streamCallback = callback;
        return { turnID };
      },
      resumeTurn() {
        resumeCalls += 1;
        throw new Error("THREAD_BUSY discovery must never replay automatically");
      },
      async cancelTurn(candidate) {
        return { turnID: candidate, canceled: true };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = interrupted.user_text;
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  streamCallback({
    type: "done",
    turnID,
    result: { code: "THREAD_BUSY", subtype: "thread_busy", is_error: true },
  });
  await settle();
  await settle();

  assert.equal(startCalls, 1);
  assert.equal(resumeCalls, 0, "initial THREAD_BUSY must never replay automatically");
  assert.equal(recoveryReads, 2, "initial THREAD_BUSY must refresh recoverable turns once");
  assert.equal(messageReads, 1, "THREAD_BUSY must not reconcile as a completed response");
  assert.equal(document.byId.get("turn-state").textContent, "Interrupted");
  assert.notEqual(document.byId.get("turn-state").textContent, "Done");
  assert.equal(document.byId.get("turn-recovery-card").hidden, false);
  assert.match(document.byId.get("status-card").textContent, /still busy.*Resume/i);
  assert.equal(ns.state.recoverableTurns.length, 1);
  assert.equal(
    ns.state.recoverableTurns[0].turn_uuid,
    turnID,
    "a stale recoverable list must not discard the immutable local intent"
  );
  await settle();
  assert.equal(resumeCalls, 0);
}

async function testCancelAckFailureShowsLocalStopAndRefreshesRecovery() {
  const turnID = "123e4567-e89b-42d3-a456-426614174012";
  const interrupted = recoverableTurn({
    turn_uuid: turnID,
    thread_uuid: "cancel-ack-thread",
    user_text: "Stop this request",
    last_error_kind: "transport_stopped",
  });
  let streamCallback;
  let cancelCalls = 0;
  let recoveryReads = 0;
  let resumeCalls = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("cancel-ack-thread", "Cancel acknowledgment")] });
      }
      if (pathname === "/agent/threads/cancel-ack-thread/messages") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        recoveryReads += 1;
        return typedSuccess({ items: [], count: 0 });
      },
      startTurn(_input, callback) {
        streamCallback = callback;
        return { turnID };
      },
      resumeTurn() {
        resumeCalls += 1;
        throw new Error("cancel recovery discovery must never replay automatically");
      },
      async cancelTurn(candidate) {
        cancelCalls += 1;
        streamCallback({ type: "canceled", turnID: candidate });
        throw new Error("Sidecar cancel acknowledgment unavailable");
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = interrupted.user_text;
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  document.byId.get("stop-button").click();
  await settle();
  await settle();

  assert.equal(cancelCalls, 1);
  assert.equal(resumeCalls, 0);
  assert.equal(recoveryReads, 2, "cancel ACK failure must refresh recoverable turns once");
  assert.match(document.byId.get("turn-state").textContent, /^Stopped locally · \d+s$/);
  assert.match(
    document.byId.get("status-card").textContent,
    /stopped locally.*persistent dismissal was not confirmed/i
  );
  assert.equal(document.byId.get("turn-recovery-card").hidden, false);
  assert.equal(
    ns.state.recoverableTurns[0].last_error_kind,
    "cancel_unconfirmed"
  );
  await settle();
  assert.equal(resumeCalls, 0, "cancel ACK failure must never replay automatically");
}

async function testSSESessionChangedClearsPromptWithoutReplay() {
  let streamCallback;
  let starts = 0;
  let threadReads = 0;
  let authReads = 0;
  let skillReads = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        authReads += 1;
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        threadReads += 1;
        return response({
          items:
            threadReads === 1
              ? [thread("old-account-thread", "Old account thread")]
              : [thread("new-account-thread", "New account thread")],
        });
      }
      if (pathname === "/agent/threads/old-account-thread/messages") {
        return response({ items: [message("old-message", "Old", "Old cached answer")] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        skillReads += 1;
        return typedSuccess(pptCatalog());
      },
      startTurn(_input, callback) {
        starts += 1;
        streamCallback = callback;
        return { turnID: "session-turn" };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const prompt = "prompt-must-never-cross-account";
  const input = document.byId.get("chat-input");
  input.value = prompt;
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  streamCallback({ type: "text_delta", turnID: "session-turn", delta: "partial old answer" });
  streamCallback({
    type: "proxy_error",
    turnID: "session-turn",
    error: {
      kind: "session_changed",
      message: "session replaced",
      retryable: false,
    },
  });
  await settle();
  await settle();

  assert.equal(starts, 1, "session recovery must never replay the prompt");
  assert.equal(authReads, 2);
  assert.equal(skillReads, 2);
  assert.equal(document.byId.get("chat-input").value, "");
  assert.equal(document.byId.get("thread-panel").hidden, true);
  assert.match(document.byId.get("thread-list").textContent, /New account thread/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /partial old answer/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, new RegExp(prompt));
  assert.doesNotMatch(JSON.stringify(ns.state), new RegExp(prompt));
  assert.match(document.byId.get("status-card").textContent, /account changed/i);
  assert.match(document.byId.get("status-card").textContent, /not resent/i);

  streamCallback({
    type: "text_delta",
    turnID: "session-turn",
    delta: "late-session-delta",
  });
  assert.doesNotMatch(document.byId.get("message-list").textContent, /late-session-delta/);
}

async function testCatalog409UsesSessionChangedRecovery() {
  let skillsCalls = 0;
  let authCalls = 0;
  let threadCalls = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        authCalls += 1;
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        threadCalls += 1;
        return response({
          items: [thread(`catalog-thread-${threadCalls}`, `Catalog account ${threadCalls}`)],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        skillsCalls += 1;
        if (skillsCalls === 1) {
          return typedFailure(409, { error: "session_changed" });
        }
        return typedSuccess(pptCatalog());
      },
      startTurn() {
        throw new Error("no turn should start during catalog recovery");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  await settle();
  await settle();
  assert.equal(skillsCalls, 2);
  assert.equal(authCalls, 2);
  assert.equal(ns.state.selectedThreadUUID, null);
  assert.match(document.byId.get("thread-list").textContent, /Catalog account 2/);
  assert.match(document.byId.get("status-card").textContent, /account changed/i);
  assert.equal(document.byId.get("chat-input").disabled, true);
}

async function testRejectsMalformedAgentContractsWithoutLeakingPayload() {
  let streamCallback;
  const canceled = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("strict-thread", "Strict contracts")] });
      }
      if (pathname === "/agent/threads/strict-thread/messages") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      startTurn(_input, callback) {
        streamCallback = callback;
        return { turnID: "strict-turn" };
      },
      async cancelTurn(turnID) {
        canceled.push(turnID);
        return { turnID, canceled: true };
      },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const input = document.byId.get("chat-input");
  input.value = "Strict event";
  input.dispatch("input");
  document.byId.get("chat-form").submit();
  streamCallback({
    type: "text_delta",
    turnID: "strict-turn",
    delta: "must not render",
    private_token: "malformed-event-secret",
  });
  await settle();

  assert.deepEqual(canceled, ["strict-turn"]);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /must not render/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /malformed-event-secret/);
  assert.doesNotMatch(document.byId.get("status-card").textContent, /malformed-event-secret/);
  assert.match(document.byId.get("status-card").textContent, /invalid event/i);
  assert.match(document.byId.get("turn-state").textContent, /^Error( · \d+s)?$/);
}

async function testRejectsLegacyOpenAgentEventShapes() {
  const cases = [
    {
      name: "unknown data",
      secret: "unknown-open-secret",
      event: {
        type: "unknown",
        turnID: "strict-open-turn",
        event: "tool",
        data: { opaque_secret: "unknown-open-secret" },
      },
    },
    {
      name: "done result extras",
      secret: "done-open-secret",
      event: {
        type: "done",
        turnID: "strict-open-turn",
        result: {
          code: "OK",
          subtype: "already_processed",
          is_error: false,
          opaque_secret: "done-open-secret",
        },
      },
    },
    {
      name: "proxy details",
      secret: "proxy-open-secret",
      event: {
        type: "proxy_error",
        turnID: "strict-open-turn",
        error: {
          kind: "unknown",
          message: "Proxy failed",
          details: { opaque_secret: "proxy-open-secret" },
        },
      },
    },
  ];

  for (const testCase of cases) {
    let streamCallback;
    const canceled = [];
    const bridge = {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=true") {
          return response({ items: [thread("strict-open-thread", "Strict closed events")] });
        }
        if (pathname === "/agent/threads/strict-open-thread/messages") {
          return response({ items: [] });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };
    const desktopBridge = {
      agent: {
        async uploadThreadFile() {
          throw new Error("uploadThreadFile is not exercised by this test");
        },
        async listSkills() {
          return typedSuccess(pptCatalog());
        },
        startTurn(_input, callback) {
          streamCallback = callback;
          return { turnID: "strict-open-turn" };
        },
        async cancelTurn(turnID) {
          canceled.push(turnID);
          return { turnID, canceled: true };
        },
      },
    };

    const { document, ns } = await runRenderer(bridge, desktopBridge);
    walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
    await settle();
    const input = document.byId.get("chat-input");
    input.value = `Strict ${testCase.name}`;
    input.dispatch("input");
    document.byId.get("chat-form").submit();
    streamCallback(testCase.event);
    await settle();

    assert.deepEqual(canceled, ["strict-open-turn"], testCase.name);
    assert.match(document.byId.get("status-card").textContent, /invalid event/i);
    assert.doesNotMatch(document.byId.get("status-card").textContent, new RegExp(testCase.secret));
    assert.doesNotMatch(document.byId.get("message-list").textContent, new RegExp(testCase.secret));
  }
}

async function testRejectsMalformedCatalogResult() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("catalog-malformed", "Malformed catalog")] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        const malformed = pptCatalog();
        malformed.count = 2;
        return typedSuccess(malformed);
      },
      startTurn() {
        throw new Error("startTurn must remain disabled for malformed catalog");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  assert.match(document.byId.get("status-card").textContent, /Malformed agent skills catalog response/);
  assert.equal(document.byId.get("chat-input").disabled, true);
  assert.equal(document.byId.get("send-button").disabled, true);
}

async function testRecoverableTurnRequiresExplicitResumeAndHandlesBusy() {
  let messageReads = 0;
  let startCalls = 0;
  let resumeCalls = 0;
  let resumeCallback;
  let outcome = "busy-code";
  const recoverable = recoverableTurn();
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("thread-agent", "Quarterly deck")] });
      }
      if (pathname === "/agent/threads/thread-agent/messages") {
        messageReads += 1;
        return response({
          items: messageReads === 1
            ? [message("message-partial", "Resume the quarterly deck", "Cached partial", "partial")]
            : [message("message-final", "Resume the quarterly deck", "Recovered final")],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        return typedSuccess({ items: [recoverable], count: 1 });
      },
      startTurn() {
        startCalls += 1;
        throw new Error("recovery discovery must never start a new prompt");
      },
      resumeTurn(turnUUID, callback) {
        resumeCalls += 1;
        assert.equal(turnUUID, recoverable.turn_uuid);
        resumeCallback = callback;
        if (outcome === "busy-code") {
          callback({
            type: "done",
            turnID: turnUUID,
            result: {
              code: "THREAD_BUSY",
              subtype: "admission",
              is_error: true,
            },
          });
        } else if (outcome === "busy-subtype") {
          callback({
            type: "done",
            turnID: turnUUID,
            result: {
              code: "CONFLICT",
              subtype: "thread_busy",
              is_error: true,
            },
          });
        } else {
          callback({ type: "text_delta", turnID: turnUUID, delta: "Recovered stream" });
          callback({
            type: "done",
            turnID: turnUUID,
            result: {
              code: "OK",
              subtype: "replay",
              is_error: false,
            },
          });
        }
        return { turnID: turnUUID };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  assert.equal(resumeCalls, 0, "startup discovery must not replay automatically");
  assert.equal(startCalls, 0);
  assert.equal(document.byId.get("turn-recovery-card").hidden, true);
  assert.match(document.byId.get("thread-list").textContent, /Interrupted/);

  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  assert.equal(resumeCalls, 0, "thread selection must not replay automatically");
  assert.equal(document.byId.get("turn-recovery-card").hidden, false);
  assert.match(document.byId.get("turn-recovery-prompt").textContent, /Resume the quarterly deck/);
  assert.equal(document.byId.get("chat-input").disabled, true);
  assert.match(document.byId.get("composer-status").textContent, /resume or dismiss/i);

  document.byId.get("turn-recovery-resume-button").click();
  await settle();
  assert.equal(resumeCalls, 1);
  assert.equal(startCalls, 0);
  assert.equal(ns.state.activeTurn, null);
  assert.equal(document.byId.get("turn-recovery-card").hidden, false);
  assert.equal(document.byId.get("turn-recovery-resume-button").disabled, false);
  assert.equal(document.byId.get("turn-recovery-resume-button").focused, true);
  assert.match(document.byId.get("turn-recovery-feedback").textContent, /still busy/i);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /must-not-render/);
  await settle();
  assert.equal(resumeCalls, 1, "THREAD_BUSY must not schedule an automatic retry");

  outcome = "busy-subtype";
  document.byId.get("turn-recovery-resume-button").click();
  await settle();
  assert.equal(resumeCalls, 2);
  assert.equal(ns.state.activeTurn, null);
  assert.equal(document.byId.get("turn-recovery-card").hidden, false);
  assert.equal(document.byId.get("turn-recovery-resume-button").disabled, false);
  assert.equal(document.byId.get("turn-recovery-resume-button").focused, true);
  assert.match(document.byId.get("turn-recovery-feedback").textContent, /still busy/i);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /subtype-secret/);
  await settle();
  assert.equal(resumeCalls, 2, "thread_busy subtype must not schedule an automatic retry");

  outcome = "success";
  document.byId.get("turn-recovery-resume-button").click();
  await settle();
  await settle();
  assert.equal(resumeCalls, 3);
  assert.equal(startCalls, 0);
  assert.equal(document.byId.get("turn-recovery-card").hidden, true);
  assert.equal(ns.state.recoverableTurns.length, 0);
  assert.match(document.byId.get("turn-state").textContent, /^Done · \d+s$/);
  assert.match(document.byId.get("message-list").textContent, /Recovered final/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /must-not-render/);
  assert.equal(messageReads, 2);
  assert.ok(resumeCallback);
}

async function testRecoverableTurnDismissIsExplicitAndIdempotent() {
  let resumeCalls = 0;
  const cancelCalls = [];
  const recoverable = recoverableTurn();
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("thread-agent", "Quarterly deck")] });
      }
      if (pathname === "/agent/threads/thread-agent/messages") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        return typedSuccess({ items: [recoverable], count: 1 });
      },
      startTurn() {
        throw new Error("dismiss must not start a prompt");
      },
      resumeTurn() {
        resumeCalls += 1;
        throw new Error("dismiss must not resume");
      },
      async cancelTurn(turnID) {
        cancelCalls.push(turnID);
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  document.byId.get("turn-recovery-dismiss-button").click();
  await settle();

  assert.deepEqual(cancelCalls, [recoverable.turn_uuid]);
  assert.equal(resumeCalls, 0);
  assert.equal(document.byId.get("turn-recovery-card").hidden, true);
  assert.equal(ns.state.recoverableTurns.length, 0);
  assert.equal(document.byId.get("chat-input").disabled, false);
  assert.equal(document.byId.get("chat-input").focused, true);
  assert.match(document.byId.get("status-card").textContent, /already dismissed/i);
}

async function testRecoverableErrorResultIsSanitized() {
  const recoverable = recoverableTurn();
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("thread-agent", "Quarterly deck")] });
      }
      if (pathname === "/agent/threads/thread-agent/messages") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        return typedSuccess({ items: [recoverable], count: 1 });
      },
      startTurn() {
        throw new Error("recovery must not start a fresh prompt");
      },
      resumeTurn(turnID, callback) {
        callback({
          type: "done",
          turnID,
          result: {
            code: "PLUGIN_FAILED",
            subtype: "render",
            is_error: true,
          },
        });
        return { turnID };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  document.byId.get("turn-recovery-resume-button").click();
  await settle();

  assert.equal(ns.state.recoverableTurns.length, 0);
  assert.equal(document.byId.get("turn-recovery-card").hidden, true);
  assert.match(document.byId.get("turn-state").textContent, /^Error( · \d+s)?$/);
  assert.match(document.byId.get("status-card").textContent, /PLUGIN_FAILED.*render/);
  assert.doesNotMatch(document.byId.get("status-card").textContent, /error-result-secret/);
  assert.doesNotMatch(document.byId.get("message-list").textContent, /error-result-secret/);
}

async function testMalformedRecoverableTurnDoesNotLeakOrRender() {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("thread-agent", "Quarterly deck")] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async listRecoverableTurns() {
        return typedSuccess({
          items: [{ ...recoverableTurn(), uid: "private-recovery-uid" }],
          count: 1,
        });
      },
      startTurn() {
        throw new Error("malformed recovery must not start");
      },
      resumeTurn() {
        throw new Error("malformed recovery must not replay");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  assert.equal(ns.state.recoverableTurns.length, 0);
  assert.equal(document.byId.get("turn-recovery-card").hidden, true);
  assert.doesNotMatch(document.byId.get("thread-list").textContent, /Interrupted/);
  assert.doesNotMatch(document.byId.get("status-card").textContent, /private-recovery-uid/);
  assert.match(document.byId.get("status-card").textContent, /Malformed agent recoverable turns result/);
}

async function testCreatesThreadOnceAndFocusesComposer() {
  const createCalls = [];
  let startCalls = 0;
  const newUUID = "00000000-0000-4000-8000-000000000001";
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [] });
      }
      if (pathname === `/agent/threads/${newUUID}/messages`) {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async createThread(input) {
        createCalls.push({
          threadUUID: input.threadUUID,
          name: input.name,
          agentMode: input.agentMode,
        });
        return typedSuccess(
          {
            state: "ready",
            created: true,
            thread: createdThread(input.threadUUID, input.name, input.agentMode),
          },
          201
        );
      },
      startTurn() {
        startCalls += 1;
        throw new Error("create must not auto-send a prompt");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  assert.equal(document.byId.get("empty-title").textContent, "What should we make today?");
  assert.equal(document.byId.get("empty-new-thread-button").hidden, false);
  assert.equal(document.byId.get("empty-new-thread-button").disabled, false);
  document.byId.get("empty-new-thread-button").click();
  assert.equal(document.byId.get("new-thread-form").hidden, false);
  assert.equal(document.byId.get("new-thread-name").focused, true);
  assert.equal(document.byId.get("new-thread-name").selected, true);
  assert.equal(document.byId.get("new-thread-mode").value, "ppt");

  document.byId.get("new-thread-name").value = "Quarterly planning";
  document.byId.get("new-thread-name").dispatch("input");
  assert.equal(document.byId.get("new-thread-submit-button").disabled, false);
  document.byId.get("new-thread-form").dispatch("keydown", { key: "Enter" });
  document.byId.get("new-thread-form").submit();
  await settle();
  await settle();

  assert.deepEqual(createCalls, [
    {
      threadUUID: newUUID,
      name: "Quarterly planning",
      agentMode: "ppt",
    },
  ]);
  assert.equal(startCalls, 0);
  assert.equal(document.byId.get("new-thread-form").hidden, true);
  assert.match(document.byId.get("thread-list").textContent, /Quarterly planning/);
  assert.equal(document.byId.get("thread-title").textContent, "Quarterly planning");
  assert.equal(document.byId.get("thread-panel").hidden, false);
  assert.equal(document.byId.get("chat-input").focused, true);
  assert.equal(document.byId.get("chat-input").value, "");
  assert.equal(ns.state.selectedThreadUUID, newUUID);
  assert.equal(ns.state.createDraft, null);
}

async function testCreateRetriesKeepUUIDAndAcceptCurrentReplayRow() {
  const createCalls = [];
  let authReads = 0;
  let threadReads = 0;
  let messageReads = 0;
  const newUUID = "00000000-0000-4000-8000-000000000001";
  const existingUUID = "thread-existing";
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        authReads += 1;
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        threadReads += 1;
        return response({ items: [thread(existingUUID, "Existing thread")] });
      }
      if (pathname === `/agent/threads/${newUUID}/messages`) {
        messageReads += 1;
        return response({ items: [] });
      }
      if (pathname === `/agent/threads/${existingUUID}/messages`) {
        messageReads += 1;
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalogWithReplayMode());
      },
      async createThread(input) {
        createCalls.push({
          threadUUID: input.threadUUID,
          name: input.name,
          agentMode: input.agentMode,
        });
        if (createCalls.length === 1) {
          return typedFailure(502, {
            error: "agent_create_unavailable",
            private_detail: "create-private-secret",
            retry_with_same_uuid: true,
          });
        }
        if (createCalls.length === 2) {
          return typedSuccess(
            { state: "pending_local_sync", thread_uuid: input.threadUUID },
            202
          );
        }
        return typedSuccess({
          state: "ready",
          created: false,
          thread: createdThread(
            input.threadUUID,
            "Current cloud title",
            "ppt_revised"
          ),
        });
      },
      startTurn() {
        throw new Error("no prompt should be sent during create recovery");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  document.byId.get("new-thread-button").click();
  document.byId.get("new-thread-name").value = "Original draft title";
  document.byId.get("new-thread-name").dispatch("input");
  document.byId.get("new-thread-form").submit();
  await settle();
  assert.equal(document.byId.get("new-thread-form").hidden, false);
  assert.equal(document.byId.get("new-thread-name").disabled, true);
  assert.match(document.byId.get("new-thread-error").textContent, /same identity/i);
  assert.doesNotMatch(document.byId.get("status-card").textContent, /create-private-secret/);
  assert.equal(ns.state.createDraft.threadUUID, newUUID);

  const firstDraft = ({ ...ns.state.createDraft });
  await ns.refresh();
  await settle();
  assert.deepEqual(({ ...ns.state.createDraft }), firstDraft);
  assert.equal(document.byId.get("new-thread-form").hidden, false);
  assert.equal(authReads, 1);
  assert.equal(threadReads, 1);
  assert.equal(messageReads, 0);
  assert.match(document.byId.get("status-card").textContent, /retry or cancel.*before refreshing/i);

  const [existingThreadButton] = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button")
  );
  assert.ok(existingThreadButton);
  existingThreadButton.click();
  assert.equal(ns.state.selectedThreadUUID, null);
  assert.deepEqual(({ ...ns.state.createDraft }), firstDraft);
  assert.equal(messageReads, 0);
  assert.match(document.byId.get("status-card").textContent, /retry or cancel.*before switching/i);

  document.byId.get("new-thread-form").submit();
  await settle();
  assert.equal(document.byId.get("new-thread-submit-button").textContent, "Retry sync");
  assert.match(document.byId.get("new-thread-error").textContent, /cloud thread is ready/i);
  assert.equal(ns.state.createDraft.threadUUID, newUUID);
  await ns.refresh();
  await settle();
  assert.equal(ns.state.createDraft.threadUUID, newUUID);
  assert.equal(ns.state.createDraft.name, "Original draft title");
  assert.equal(ns.state.createDraft.agentMode, "ppt");
  assert.equal(authReads, 1);
  assert.equal(threadReads, 1);

  document.byId.get("new-thread-form").submit();
  await settle();
  await settle();
  assert.equal(createCalls.length, 3);
  for (const call of createCalls) {
    assert.deepEqual(call, {
      threadUUID: newUUID,
      name: "Original draft title",
      agentMode: "ppt",
    });
  }
  assert.equal(document.byId.get("thread-title").textContent, "Current cloud title");
  assert.equal(document.byId.get("agent-mode").value, "ppt_revised");
  assert.equal(ns.state.selectedThreadUUID, newUUID);
  assert.equal(messageReads, 1);
}

async function testPermanentCreateFailuresDoNotOfferSameIdentityRetry() {
  const cases = [
    {
      status: 401,
      error: { error: "authentication_required", retry_with_same_uuid: true },
      feedback: /authentication is required/i,
    },
    {
      status: 409,
      error: { error: "thread_uuid_conflict", retry_with_same_uuid: true },
      feedback: /already owned elsewhere/i,
    },
    {
      status: 409,
      error: { error: "local_identity_conflict", retry_with_same_uuid: true },
      feedback: /identity conflict/i,
    },
  ];

  for (const testCase of cases) {
    let authReads = 0;
    let threadReads = 0;
    let createCalls = 0;
    const bridge = {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          authReads += 1;
          return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=true") {
          threadReads += 1;
          return response({ items: [] });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };
    const desktopBridge = {
      agent: {
        async uploadThreadFile() {
          throw new Error("uploadThreadFile is not exercised by this test");
        },
        async listSkills() {
          return typedSuccess(pptCatalog());
        },
        async createThread() {
          createCalls += 1;
          return typedFailure(testCase.status, testCase.error);
        },
        startTurn() {
          throw new Error("permanent create failure must not start a turn");
        },
        async cancelTurn(turnID) {
          return { turnID, canceled: false };
        },
      },
    };

    const { context, document, ns } = await runRenderer(bridge, desktopBridge);
    document.byId.get("new-thread-button").click();
    document.byId.get("new-thread-form").submit();
    await settle();

    assert.equal(createCalls, 1);
    assert.equal(document.byId.get("new-thread-form").hidden, false);
    assert.equal(ns.state.createDraft.retryable, false);
    assert.match(document.byId.get("new-thread-error").textContent, testCase.feedback);
    assert.doesNotMatch(
      `${document.byId.get("new-thread-error").textContent} ${document.byId.get("status-card").textContent}`,
      /same identity|retry keeps/i
    );
    assert.equal(document.byId.get("new-thread-submit-button").disabled, true);
    assert.equal(document.byId.get("new-thread-submit-button").textContent, "Cannot retry");

    document.byId.get("new-thread-form").submit();
    await settle();
    assert.equal(createCalls, 1);

    await ns.refresh();
    await settle();
    assert.equal(createCalls, 1);
    assert.equal(authReads, 1);
    assert.equal(threadReads, 1);
    assert.equal(document.byId.get("new-thread-form").hidden, false);
    assert.equal(ns.state.createDraft.retryable, false);
    assert.match(document.byId.get("status-card").textContent, /cancel.*before refreshing/i);

    document.byId.get("new-thread-cancel-button").click();
    assert.equal(document.byId.get("new-thread-form").hidden, true);
    assert.equal(ns.state.createDraft, null);
  }
}

async function testPausedCreateReplayRequiresExplicitCancel() {
  let createCalls = 0;
  const newUUID = "00000000-0000-4000-8000-000000000001";
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      async createThread(input) {
        createCalls += 1;
        return typedSuccess({
          state: "ready",
          created: false,
          thread: {
            ...createdThread(input.threadUUID, "Paused cloud title", input.agentMode),
            cloud_sync_state: "paused",
          },
        });
      },
      startTurn() {
        throw new Error("a paused create replay must not start a turn");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  document.byId.get("new-thread-button").click();
  document.byId.get("new-thread-form").submit();
  await settle();

  assert.equal(createCalls, 1);
  assert.equal(ns.state.selectedThreadUUID, null);
  assert.equal(ns.state.createDraft.threadUUID, newUUID);
  assert.equal(ns.state.createDraft.retryable, false);
  assert.equal(document.byId.get("new-thread-form").hidden, false);
  assert.equal(document.byId.get("new-thread-submit-button").disabled, true);
  assert.match(document.byId.get("new-thread-error").textContent, /paused.*cancel/i);
  assert.doesNotMatch(document.byId.get("thread-list").textContent, /Paused cloud title/);

  document.byId.get("new-thread-form").submit();
  await settle();
  assert.equal(createCalls, 1);

  document.byId.get("new-thread-cancel-button").click();
  assert.equal(document.byId.get("new-thread-form").hidden, true);
  assert.equal(ns.state.createDraft, null);
}

async function testCreateEscapeFencesLateCompletion() {
  let resolveCreate;
  let createCalls = 0;
  const newUUID = "00000000-0000-4000-8000-000000000001";
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      createThread() {
        createCalls += 1;
        return new Promise((resolve) => {
          resolveCreate = resolve;
        });
      },
      startTurn() {
        throw new Error("late create must not start a turn");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  document.byId.get("new-thread-button").click();
  document.byId.get("new-thread-form").submit();
  document.byId.get("new-thread-form").dispatch("keydown", { key: "Escape" });
  assert.equal(document.byId.get("new-thread-form").hidden, true);
  assert.match(document.byId.get("status-card").textContent, /late result will be ignored/i);

  resolveCreate(
    typedSuccess(
      {
        state: "ready",
        created: true,
        thread: createdThread(newUUID, "Untitled presentation"),
      },
      201
    )
  );
  await settle();
  assert.equal(createCalls, 1);
  assert.equal(ns.state.selectedThreadUUID, null);
  assert.equal(ns.state.createDraft, null);
  assert.doesNotMatch(document.byId.get("thread-list").textContent, /Untitled presentation/);
}

async function testCreateSessionChangedUsesUnifiedRecovery() {
  let authReads = 0;
  let threadReads = 0;
  let skillReads = 0;
  let createCalls = 0;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        authReads += 1;
        return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        threadReads += 1;
        return response({ items: [] });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        skillReads += 1;
        return typedSuccess(pptCatalog());
      },
      async createThread() {
        createCalls += 1;
        return typedFailure(409, { error: "session_changed" });
      },
      startTurn() {
        throw new Error("session recovery must not start a turn");
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: false };
      },
    },
  };

  const { context, document, ns } = await runRenderer(bridge, desktopBridge);
  document.byId.get("new-thread-button").click();
  document.byId.get("new-thread-form").submit();
  await settle();
  await settle();

  assert.equal(createCalls, 1);
  assert.equal(authReads, 2);
  assert.equal(threadReads, 2);
  assert.equal(skillReads, 2);
  assert.equal(document.byId.get("new-thread-form").hidden, true);
  assert.equal(ns.state.createDraft, null);
  assert.match(document.byId.get("status-card").textContent, /account changed/i);
  assert.match(document.byId.get("status-card").textContent, /creation was not replayed/i);
}

async function testCreateRejectsForeignUUIDAndMode() {
  const invalidThreads = [
    createdThread("00000000-0000-4000-8000-000000000099", "Foreign UUID"),
    createdThread(
      "00000000-0000-4000-8000-000000000001",
      "Foreign mode",
      "writer"
    ),
  ];
  for (const invalidThread of invalidThreads) {
    const bridge = {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          return response({ state: "authenticated", updated_at: "2026-08-06T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=true") {
          return response({ items: [] });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    };
    const desktopBridge = {
      agent: {
        async uploadThreadFile() {
          throw new Error("uploadThreadFile is not exercised by this test");
        },
        async listSkills() {
          return typedSuccess(pptCatalog());
        },
        async createThread() {
          return typedSuccess({
            state: "ready",
            created: false,
            thread: invalidThread,
          });
        },
        startTurn() {
          throw new Error("malformed create response must not start a turn");
        },
        async cancelTurn(turnID) {
          return { turnID, canceled: false };
        },
      },
    };

    const { context, document, ns } = await runRenderer(bridge, desktopBridge);
    document.byId.get("new-thread-button").click();
    document.byId.get("new-thread-form").submit();
    await settle();
    assert.equal(ns.state.selectedThreadUUID, null);
    assert.match(document.byId.get("new-thread-error").textContent, /same identity/i);
    assert.doesNotMatch(document.byId.get("thread-list").textContent, /Foreign/);
  }
}

// --- Appearance --------------------------------------------------------------
//
// The cascade itself is checked against styles.css at the top of this file.
// What is checked here is the hook: which attribute lands on <html>, when, and
// what survives a relaunch.
//
// "Survives a relaunch" changed shape and is the whole point of the exercise.
// The preference used to be written to localStorage, which is scoped to an
// origin — and this app's UI origin binds a new port on every launch, so the
// theme was written to a store no later launch could ever read. It now goes to
// the sidecar through settings.putAppearance, and comes back on the NEXT
// launch as data-theme on the served <html>, which is what the `theme` option
// below stands for. There is no read path in the renderer at all: a page that
// asked would be a page that repaints.
async function testAppearanceIsThreeStateAndPersisted() {
  const saved = [];
  const appearanceBridge = () => ({
    settings: {
      async putAppearance(choice) {
        saved.push(choice);
        return typedSuccess({
          appearance: choice,
          density: "standard",
          updated_at: "2026-08-11T00:00:00Z",
        });
      },
    },
  });
  const { document } = await runRenderer(undefined, appearanceBridge());

  // Default is "follow the system", expressed as the ABSENCE of the attribute
  // rather than data-theme="system": the media query already is the system
  // answer, and a third attribute value would have to be excluded by hand from
  // every guard in the stylesheet.
  assert.equal(
    document.documentElement.getAttribute("data-theme"),
    null,
    "an app that has never been told otherwise must follow the system",
  );

  const openPalette = () => document.dispatchKey({ key: "k", metaKey: true });
  const commands = () =>
    walk(
      document.byId.get("quick-switcher-list"),
      (node) => node.classList?.contains("quick-switcher-command"),
    ).map((node) => node.textContent);
  const runCommand = (prefix) => {
    const button = walk(
      document.byId.get("quick-switcher-list"),
      (node) => node.classList?.contains("quick-switcher-command"),
    ).find((node) => node.textContent.startsWith(prefix));
    assert.ok(button, `expected a palette command starting with "${prefix}"`);
    button.click();
  };

  openPalette();
  for (const label of ["Appearance: match system", "Appearance: light", "Appearance: dark"]) {
    assert.ok(
      commands().some((entry) => entry.startsWith(label)),
      `the palette must offer "${label}"`,
    );
  }
  assert.ok(
    commands().some((entry) => entry.startsWith("Appearance: match system") && entry.includes("Current")),
    "the active appearance must say so instead of being hidden",
  );

  runCommand("Appearance: dark");
  assert.equal(document.documentElement.getAttribute("data-theme"), "dark");
  assert.equal(document.byId.get("quick-switcher").hidden, true, "choosing closes the palette");
  // The window changes at once; the write follows without anything waiting on
  // it. Both halves matter: a switch that hesitated on a database would be a
  // worse switch than the one that used to forget.
  await settle();
  assert.deepEqual(saved, ["dark"], "the choice has to be handed to the sidecar to outlive the window");

  openPalette();
  assert.ok(
    commands().some((entry) => entry.startsWith("Appearance: dark") && entry.includes("Current")),
    "the palette reflects the choice that was just made",
  );
  runCommand("Appearance: light");
  assert.equal(document.documentElement.getAttribute("data-theme"), "light");

  openPalette();
  runCommand("Appearance: match system");
  assert.equal(
    document.documentElement.getAttribute("data-theme"),
    null,
    "going back to the system removes the attribute rather than setting a third value",
  );
  await settle();
  assert.deepEqual(saved, ["dark", "light", "system"], "every state is written, including the default");

  // A relaunch. The document arrives already dark — the shell resolved the
  // stored preference while serving index.html — and the renderer's job is to
  // agree with it rather than to go and ask, which is what keeps a light frame
  // from ever being painted.
  const relaunched = await runRenderer(undefined, appearanceBridge(), { theme: "dark" });
  assert.equal(relaunched.document.documentElement.getAttribute("data-theme"), "dark");
  relaunched.document.dispatchKey({ key: "k", metaKey: true });
  assert.ok(
    walk(
      relaunched.document.byId.get("quick-switcher-list"),
      (node) => node.classList?.contains("quick-switcher-command"),
    )
      .map((node) => node.textContent)
      .some((entry) => entry.startsWith("Appearance: dark") && entry.includes("Current")),
    "a relaunched window knows which appearance it is showing",
  );

  // Anything else in the attribute is a shell that has drifted or markup that
  // was tampered with; it means "no opinion", never a third state on the page.
  const garbage = await runRenderer(undefined, appearanceBridge(), { theme: "<script>" });
  assert.equal(
    garbage.document.documentElement.getAttribute("data-theme"),
    null,
    "an unrecognised served value falls back to the system, never onto the page",
  );

  // And against a shell with no appearance route at all, the app still
  // switches — it just will not remember. Every other test in this file runs
  // in that shape; this is the one that says so on purpose.
  const noBridge = await runRenderer(undefined);
  assert.equal(noBridge.document.documentElement.getAttribute("data-theme"), null);
  noBridge.document.dispatchKey({ key: "k", metaKey: true });
  walk(
    noBridge.document.byId.get("quick-switcher-list"),
    (node) => node.classList?.contains("quick-switcher-command"),
  )
    .find((node) => node.textContent.startsWith("Appearance: dark"))
    .click();
  await settle();
  assert.equal(noBridge.document.documentElement.getAttribute("data-theme"), "dark");
}

// --- Density is the same shape as appearance, deliberately -------------------
//
// Machine-scoped, stored by the sidecar, stamped onto <html> by the shell
// before first paint, default expressed as the ABSENCE of the attribute. It is
// worth checking it really is the same shape, because the value of a second
// preference that behaves like the first is entirely in the "like the first".
async function testDensityIsThreeStateAndPersisted() {
  const saved = [];
  const { document } = await runRenderer(undefined, {
    settings: {
      async putDensity(choice) {
        saved.push(choice);
        return typedSuccess({
          appearance: "system",
          density: choice,
          updated_at: "2026-08-11T00:00:00Z",
        });
      },
    },
  });

  assert.equal(
    document.documentElement.getAttribute("data-density"),
    null,
    "a machine that has never been asked packs at the standard density",
  );

  const segment = (id) => {
    const button = document.byId.get(id);
    assert.ok(button, `the density control must offer ${id}`);
    return button;
  };
  const marked = () =>
    ["density-compact", "density-standard", "density-comfortable"]
      .filter((id) => segment(id).classList.contains("active"));

  assert.deepEqual(marked(), ["density-standard"], "the live density names itself");

  segment("density-compact").click();
  assert.equal(document.documentElement.getAttribute("data-density"), "compact");
  assert.deepEqual(marked(), ["density-compact"]);
  assert.equal(
    segment("density-compact").getAttribute("aria-pressed"),
    "true",
    "a segmented control has to say which segment is on, not only look like it",
  );
  await settle();
  assert.deepEqual(saved, ["compact"], "the choice has to outlive the window");

  // Standard removes the attribute rather than writing it: the stylesheet
  // already IS the standard answer, and a value meaning "the default" would
  // have to be excluded by hand from every guard that keys on this attribute.
  segment("density-standard").click();
  assert.equal(document.documentElement.getAttribute("data-density"), null);
  await settle();
  assert.deepEqual(saved, ["compact", "standard"]);

  // Changing the density must not disturb the theme — they share a row on the
  // sidecar, and this is the renderer half of that guarantee.
  const themed = await runRenderer(undefined, {
    settings: {
      async putDensity() {
        return typedSuccess({
          appearance: "dark",
          density: "comfortable",
          updated_at: "2026-08-11T00:00:00Z",
        });
      },
    },
  }, { theme: "dark" });
  themed.document.byId.get("density-comfortable").click();
  await settle();
  assert.equal(themed.document.documentElement.getAttribute("data-theme"), "dark");
  assert.equal(themed.document.documentElement.getAttribute("data-density"), "comfortable");

  // A shell too old to have the route still switches; it just will not
  // remember. Saying so on every click would be noise about a build the reader
  // is not running.
  const noBridge = await runRenderer(undefined);
  noBridge.document.byId.get("density-compact").click();
  await settle();
  assert.equal(noBridge.document.documentElement.getAttribute("data-density"), "compact");
}

// --- An answer keeps saying where it came from -------------------------------
//
// This is the whole reason provenance is stored per message instead of being
// drawn from the current settings. The renderer knows which engine is
// configured NOW; what it cannot know, without being told per turn and having
// it written down, is what produced the answer already on screen. Switch
// engines mid-conversation and every earlier reply would silently start
// claiming to have come from the new one.
//
// The live frame is only half of it. A finished turn is reconciled against the
// sidecar's cached rows, and the app is reopened from those rows alone — so a
// claim that lived only on the streamed nodes would vanish at exactly the
// moment someone scrolls back to check it. This drives the durable path.
async function testAnswersKeepNamingTheirOwnOrigin() {
  let emit = () => {};
  let turnSeq = 0;
  // What the sidecar has cached, which is what a rebuilt transcript is made
  // of. Turns append to it as they complete, each carrying its own engine.
  const cached = [];
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-13T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("provenance-thread", "Provenance")] });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: cached.slice() });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async listSkills() { return typedSuccess(pptCatalog()); },
      async uploadThreadFile() { throw new Error("not exercised"); },
      startTurn(input, callback) {
        turnSeq += 1;
        const turnID = `prov-turn-${turnSeq}`;
        emit = (event) => callback({ ...event, turnID });
        return { turnID };
      },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
  };

  const { document } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();

  const provenance = () =>
    walk(document.byId.get("message-list"), (n) =>
      n.classList?.contains("message-provenance"),
    ).map((n) => n.textContent);

  // One turn: announced live, then cached with what the announcement said.
  const runTurn = async (ask, answer, meta) => {
    const input = document.byId.get("chat-input");
    input.value = ask;
    input.dispatch("input");
    document.byId.get("chat-form").submit();
    await settle();
    if (meta) emit({ type: "turn_meta", ...meta });
    emit({ type: "text_delta", delta: answer });
    await settle();
    cached.push(message(`m-${cached.length + 1}`, ask, answer, "complete", meta ?? {}));
    emit({ type: "done", result: { code: "", subtype: "", is_error: false } });
    await settle();
  };

  await runTurn("First question", "First answer.", {
    engine: "claude",
    model: "claude-sonnet-4.6",
    mind: "General mind",
  });
  // One sentence, mind first: it is what identifies the answer, because two
  // minds usually share a model and the model alone cannot tell them apart.
  assert.deepEqual(provenance(), ["General mind · claude · claude-sonnet-4.6"]);

  // The settings then change and the next turn runs somewhere else entirely.
  // The first answer must not be rewritten by what the second one ran on.
  await runTurn("Second question", "Second answer.", {
    engine: "pi",
    model: "qwen3-coder",
    mind: "Payroll mind",
  });
  assert.deepEqual(provenance(), [
    "General mind · claude · claude-sonnet-4.6",
    "Payroll mind · pi · qwen3-coder",
  ]);

  // The case the mind exists FOR: same engine, same model, different mind.
  // Without the mind these two answers would be indistinguishable, which is
  // the whole reason 0014 exists.
  await runTurn("Same model, other mind", "Third answer.", {
    engine: "pi",
    model: "qwen3-coder",
    mind: "Research mind",
  });
  assert.equal(provenance()[2], "Research mind · pi · qwen3-coder");

  // An engine that chose its own default names only itself. Inventing a model
  // nobody picked is the failure this whole line exists to prevent.
  await runTurn("No model chosen", "Fourth answer.", { engine: "pi", model: "", mind: "" });
  assert.equal(provenance()[3], "pi");

  // A turn the sidecar never announced says nothing, rather than guessing from
  // the current settings — which is the lie this replaces.
  await runTurn("Unannounced", "Fifth answer.", null);
  assert.equal(provenance().length, 4, "no announcement means no claim");

  // And the part a live-only footer could never do: reopen the conversation.
  // Everything on screen now came from the cache, and it still knows.
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  assert.deepEqual(
    provenance(),
    [
      "General mind · claude · claude-sonnet-4.6",
      "Payroll mind · pi · qwen3-coder",
      "Research mind · pi · qwen3-coder",
      "pi",
    ],
    "a transcript rebuilt from cache must still say what produced each answer",
  );

  // A row whose provenance is not a string this page can safely print reads as
  // no claim at all. It arrives from the sidecar's own database, which is
  // exactly the kind of "trusted" source that stops being trusted the moment
  // anything else can write to it.
  cached.length = 0;
  cached.push({
    ...message("m-hostile", "Hostile", "Answer."),
    agent_engine: "claude\u0000<img>",
    agent_model: "m",
  });
  cached.push({
    ...message("m-wrongtype", "Wrong type", "Answer."),
    agent_engine: 42,
    agent_model: null,
  });
  cached.push({
    ...message("m-long", "Long", "Answer."),
    agent_engine: "e".repeat(64),
    agent_model: "m".repeat(200),
  });
  walk(document.byId.get("thread-list"), (node) => node.tagName === "BUTTON")[0].click();
  await settle();
  const hostile = provenance();
  assert.equal(hostile.length, 1, `only the printable row may make a claim: ${JSON.stringify(hostile)}`);
  assert.equal(hostile[0].length, 32 + 3 + 80, "an over-long pair is clipped to what the layout can hold");

  // It is a footnote, not a control: always present, never revealed on hover.
  // Provenance you have to go looking for is provenance nobody checks.
  assert.match(
    rendererCSS,
    /\.message-provenance \{[^}]*?font-size: var\(--fs-micro\);/u,
    "the provenance line is the quietest type in the sheet",
  );
  assert.doesNotMatch(
    rendererSource,
    /message-provenance reveal-on-hover/u,
    "provenance is not a hover affordance",
  );
}

// --- Syntax highlighting -----------------------------------------------------
async function testCodeTokenizerClassifiesTheLanguagesWeShip() {
  const { context, ns } = await runRenderer(undefined);
  const tokenize = (code, language) => ns.tokenizeCode(code, language);

  // Every case asserts two things: the classification, and that the runs
  // concatenate back to the input character for character. The second is what
  // makes a highlighted block still selectable and copyable as the code it is —
  // a tokenizer that loses a space is a tokenizer that corrupts what the reader
  // pastes into a terminal.
  const classify = (code, language) => {
    const tokens = tokenize(code, language);
    assert.ok(tokens, `expected ${language} to be highlighted`);
    assert.equal(tokens.map((token) => token.text).join(""), code, "tokens must cover the input");
    return new Map(tokens.filter((token) => token.type).map((token) => [token.text, token.type]));
  };

  const js = classify('const answer = fn(1, "two"); // note\n', "js");
  assert.equal(js.get("const"), "keyword");
  assert.equal(js.get("fn"), "function");
  assert.equal(js.get("1"), "number");
  assert.equal(js.get('"two"'), "string");
  assert.equal(js.get("// note"), "comment");
  // ts is the same grammar; the alias has to resolve or a ```ts block is plain.
  assert.equal(classify("interface A {}", "ts").get("interface"), "keyword");

  const py = classify('def go(x):\n    return "s"  # why\n', "python");
  assert.equal(py.get("def"), "keyword");
  assert.equal(py.get("go"), "function");
  assert.equal(py.get('"s"'), "string");
  assert.equal(py.get("# why"), "comment");

  const go = classify("func main() {\n\ts := `raw`\n\t_ = len(s)\n}\n", "go");
  assert.equal(go.get("func"), "keyword");
  assert.equal(go.get("`raw`"), "string");
  assert.equal(go.get("len"), "builtin");

  const json = classify('{"name": "x", "ok": true, "n": 12}', "json");
  assert.equal(json.get('"name"'), "attr", "a key is not the same thing as a value");
  assert.equal(json.get('"x"'), "string");
  assert.equal(json.get("true"), "keyword");
  assert.equal(json.get("12"), "number");

  // SQL keywords are case-insensitive and people write them both ways.
  for (const code of ["select id from t -- c", "SELECT id FROM t -- c"]) {
    const sql = classify(code, "sql");
    assert.equal(sql.get(code.slice(0, 6)), "keyword");
    assert.equal(sql.get("-- c"), "comment");
  }

  const bash = classify('echo "$HOME/bin" # go\n', "bash");
  assert.equal(bash.get("echo"), "builtin");
  assert.equal(bash.get("# go"), "comment");
  assert.equal(classify("cd $HOME\n", "bash").get("$HOME"), "builtin");

  const html = classify('<a href="x" data-y=\'1\'>t</a>', "html");
  assert.equal(html.get("<a"), "tag");
  assert.equal(html.get("href"), "attr");
  assert.equal(html.get('"x"'), "string");
  assert.equal(html.get("</a"), "tag");

  const css = classify(".x { color: #fff; --gap: 4px }", "css");
  assert.equal(css.get(".x"), "tag");
  assert.equal(css.get("color"), "attr");
  assert.equal(css.get("#fff"), "number");
  assert.equal(css.get("--gap"), "attr");

  const md = classify("# Title\n\n- a `snippet` and [link](http://x)\n", "markdown");
  assert.equal(md.get("# Title"), "keyword");
  assert.equal(md.get("`snippet`"), "string");
  assert.equal(md.get("[link](http://x)"), "function");

  // A language with no grammar keeps its plain text and its badge. Refusing is
  // the answer, not guessing with the nearest grammar.
  assert.equal(tokenize("fn main() {}", "rust"), null);
  assert.equal(tokenize("x", ""), null);
  assert.equal(tokenize("x", undefined), null);

  // Broken input must terminate and must not swallow the rest of the block: an
  // unclosed quote colours its own line and stops at the newline.
  const unterminated = classify('let a = "oops\nlet b = 2;\n', "js");
  assert.equal(unterminated.get("let"), "keyword");
  assert.equal(unterminated.get("2"), "number", "the line after a broken string still parses");
}

async function testCodeBlocksArePaintedWithoutMarkup() {
  const { context, document, ns } = await runRenderer(undefined);
  const render = (markdown) => {
    const container = document.createElement("div");
    ns.renderMarkdownInto(container, markdown);
    return container;
  };
  const tokenSpans = (root) =>
    walk(root, (node) => typeof node.className === "string" && node.className.startsWith("tok-"));

  const code = 'const x = 1; // <img src=x onerror=alert(1)>\n';
  const container = render("```js\n" + code + "```\n");
  const codeNode = walk(container, (node) => node.tagName === "CODE")[0];
  assert.ok(codeNode, "expected a code element");
  assert.equal(codeNode.textContent, code.trimEnd(), "plain text is on screen immediately");
  assert.equal(tokenSpans(container).length, 0, "highlighting must not block the first paint");

  await settle();
  const spans = tokenSpans(container);
  assert.ok(spans.length >= 3, "the block is coloured once the idle pass runs");
  assert.equal(
    codeNode.textContent,
    code.trimEnd(),
    "and the text is byte-identical afterwards — the reader copies code, not spans",
  );
  // The would-be tag inside the comment is still text. It always was; the point
  // is that adding a highlighter did not quietly change that.
  assert.ok(
    walk(container, (node) => node.tagName === "IMG").length === 0,
    "model output must never become an element",
  );
  for (const span of spans) {
    assert.match(span.className, /^tok-[a-z]+$/u, "token classes are a fixed vocabulary");
  }

  // Bounds. Past them the block keeps its plain text, in the same spirit as
  // MARKDOWN_MAX_CHARS: a wall this size is a data dump, not something a person
  // reads, and colouring it would cost far more than it returns.
  const huge = "x = 1;\n".repeat(15_000); // > 100 KB and > 5000 lines
  const hugeContainer = render("```js\n" + huge + "```\n");
  await settle();
  assert.equal(tokenSpans(hugeContainer).length, 0, "an oversized block stays plain");
  assert.equal(
    walk(hugeContainer, (node) => node.tagName === "CODE")[0].textContent,
    huge.trimEnd(),
    "and keeps every character of it",
  );

  const tallButSmall = "a\n".repeat(6_000); // under the byte bound, over the line bound
  const tallContainer = render("```python\n" + tallButSmall + "```\n");
  await settle();
  assert.equal(tokenSpans(tallContainer).length, 0, "the line bound applies on its own");
}


// --- Transcript windowing ----------------------------------------------------
//
// Opening a long conversation must not build a thousand articles. What follows
// pins the window (how much is mounted), the control (how the rest is reached),
// the scroll anchor (where the reader ends up after reaching it), and the two
// things the window could plausibly have broken: the in-place post-turn
// reconcile, and "jump to latest".

// A thread whose history is `count` exchanges long, newest last.
function longThreadBridge(count, threadUUID = "long-thread") {
  const items = [];
  for (let i = 0; i < count; i += 1) {
    items.push(message(`m-${i}`, `Prompt ${i}`, `Answer ${i}`));
  }
  return {
    items,
    bridge: {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=true") {
          return response({ items: [thread(threadUUID, "Long thread")] });
        }
        if (pathname === `/agent/threads/${threadUUID}/messages`) {
          return response({ items });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    },
  };
}

const messageArticles = (document) =>
  walk(document.byId.get("message-list"), (node) => node.classList?.contains("message"));
const earlierButton = (document) =>
  walk(document.byId.get("message-list"), (node) =>
    node.classList?.contains("transcript-earlier"),
  )[0] ?? null;

async function openLongThread(count) {
  const { bridge } = longThreadBridge(count);
  const { document, ns } = await runRenderer(bridge);
  const threadButton = walk(
    document.byId.get("thread-list"),
    (node) => node.classList?.contains("thread-button"),
  )[0];
  threadButton.click();
  await settle();
  return { document, ns };
}

async function testLongTranscriptMountsAWindowNotTheWholeHistory() {
  const { document, ns } = await openLongThread(120);
  const window = ns.TRANSCRIPT_WINDOW_TURNS;
  assert.ok(window > 0 && window < 120, "the fixture must be longer than one window");

  // Two articles per exchange, and only for the window.
  assert.equal(
    messageArticles(document).length,
    window * 2,
    "opening a long thread must mount one window, not the whole history",
  );
  // Which exchanges are mounted is pinned by position rather than by
  // searching the text: "does the list mention Prompt 0" is the assertion that
  // silently stops meaning anything the day a prompt reads "Prompt 01".
  const articles = messageArticles(document);
  assert.ok(articles[0].textContent.includes("Prompt 80"), "the window starts at the 41st-newest");
  assert.ok(
    articles[articles.length - 1].textContent.includes("Answer 119"),
    "the reader lands on the newest exchange, as before",
  );
  // And the oldest is genuinely absent from the DOM, not merely hidden — the
  // whole point is that its nodes were never built.
  assert.ok(
    !articles.some((node) => node.textContent.includes("Prompt 0Edit")),
    "the oldest exchange must have no nodes at all",
  );

  const control = earlierButton(document);
  assert.ok(control, "a truncated transcript must offer a way back");
  assert.equal(control.textContent, `Show ${120 - window} earlier exchanges`);
  assert.equal(
    document.byId.get("message-list").children[0],
    control,
    "the control belongs above the oldest mounted message",
  );
}

async function testShortTranscriptHasNoWindowControl() {
  const { document, ns } = await openLongThread(3);
  assert.equal(messageArticles(document).length, 6);
  assert.equal(earlierButton(document), null, "a short thread must not grow a control");
  assert.match(document.byId.get("message-list").textContent, /Prompt 0/);
}

async function testShowEarlierPrependsWithoutRebuildingWhatIsMounted() {
  const { document, ns } = await openLongThread(120);
  const window = ns.TRANSCRIPT_WINDOW_TURNS;
  const list = document.byId.get("message-list");

  // Node identity is the assertion that matters: a repaint would satisfy every
  // count while destroying a streaming answer, an open approval card and the
  // reader's place in the page.
  const before = messageArticles(document);
  const newestBefore = before[before.length - 1];
  const oldestMountedBefore = before[0];

  earlierButton(document).click();
  await settle();

  const after = messageArticles(document);
  assert.equal(after.length, window * 4, "one more window's worth is mounted");
  assert.equal(after[after.length - 1], newestBefore, "the newest node was not rebuilt");
  assert.equal(
    after[window * 2],
    oldestMountedBefore,
    "the previously mounted rows kept their nodes and their order",
  );
  assert.ok(
    after[0].textContent.includes("Prompt 40"),
    "the window now starts one window further back",
  );
  void list;
  assert.equal(earlierButton(document).textContent, `Show ${120 - window * 2} earlier exchanges`);
}

async function testShowEarlierHoldsTheScrollAnchorAndRetiresItselfAtTheTop() {
  const { document, ns } = await openLongThread(100);
  const viewport = document.byId.get("message-viewport");
  const window = ns.TRANSCRIPT_WINDOW_TURNS;

  // Put the reader somewhere in the middle, then grow the document above them.
  viewport.scrollTop = 500;
  const heightBefore = viewport.scrollHeight;
  earlierButton(document).click();
  await settle();
  const grew = viewport.scrollHeight - heightBefore;
  assert.ok(grew > 0, "mounting earlier messages must make the document taller");
  assert.equal(
    viewport.scrollTop,
    500 + grew,
    "the reader must stay on the same message: scroll moves by exactly what appeared above",
  );

  // Walk to the top. The control removes itself when there is nothing left
  // behind it, rather than sitting there claiming zero.
  while (earlierButton(document)) {
    earlierButton(document).click();
    await settle();
  }
  const all = messageArticles(document);
  assert.equal(all.length, 200, "everything is mounted at the top");
  assert.ok(all[0].textContent.includes("Prompt 0Edit"), "the very first exchange is mounted");
  assert.equal(earlierButton(document), null);
  void window;
}


// The window and the in-place reconcile have to work together, and the way
// they could fail is specific: the reconcile refuses unless the transcript's
// row count matches the snapshot, and in a windowed transcript most of the
// snapshot is deliberately not mounted. Get that wrong and every long
// conversation silently falls back to the full repaint — the exact cost the
// window exists to remove, now paid on every single turn instead of once.
async function testCompletedTurnReconcilesInPlaceInsideTheWindow() {
  const history = [];
  for (let i = 0; i < 100; i += 1) history.push(message(`m-${i}`, `Prompt ${i}`, `Answer ${i}`));
  let messageReads = 0;
  let streamCallback;
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-05-21T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({ items: [thread("thread-long", "Long thread", history.length)] });
      }
      if (pathname === "/agent/threads/thread-long/messages") {
        messageReads += 1;
        return response({
          items:
            messageReads === 1
              ? history
              : [...history, message("m-new", "One more", "One more answer")],
        });
      }
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() {
        throw new Error("uploadThreadFile is not exercised by this test");
      },
      async listSkills() {
        return typedSuccess(pptCatalog());
      },
      startTurn(input, callback) {
        streamCallback = callback;
        return { turnID: "turn-long" };
      },
      async cancelTurn(turnID) {
        return { turnID, canceled: true };
      },
    },
  };

  const { document, ns } = await runRenderer(bridge, desktopBridge);
  walk(document.byId.get("thread-list"), (n) => n.classList?.contains("thread-button"))[0].click();
  await settle();

  const window = ns.TRANSCRIPT_WINDOW_TURNS;
  assert.equal(messageArticles(document).length, window * 2, "the long thread opened windowed");
  const control = earlierButton(document);
  assert.ok(control, "and with a control");
  // A node from the middle of the window. If the reconcile falls back to the
  // full repaint, this node is replaced and the identity check below fails —
  // which is the whole point of the assertion.
  const survivor = messageArticles(document)[0];

  const chatInput = document.byId.get("chat-input");
  chatInput.value = "One more";
  chatInput.dispatch("input");
  document.byId.get("chat-form").submit();
  await settle();

  streamCallback({ type: "text_delta", turnID: "turn-long", delta: "One more answer" });
  await settle();
  streamCallback({
    type: "done",
    turnID: "turn-long",
    result: { code: "OK", subtype: "", is_error: false },
  });
  await settle();
  await settle();

  const after = messageArticles(document);
  assert.equal(after.length, window * 2 + 2, "the new exchange joined the window");
  assert.equal(after[0], survivor, "the reconcile stayed in place: no repaint of the window");
  assert.equal(earlierButton(document), control, "and the control was not rebuilt either");
  assert.ok(
    after[after.length - 1].textContent.includes("One more answer"),
    "the streamed answer is the transcript's last row",
  );

  // The bookkeeping has to keep up too, or the NEXT turn counts against a
  // conversation one exchange shorter than what is on screen and falls back.
  earlierButton(document).click();
  await settle();
  assert.equal(
    messageArticles(document).length,
    window * 4 + 2,
    "showing earlier still lines up with the snapshot after an in-place reconcile",
  );
}


// --- Official model selection ------------------------------------------------
//
// The official route used to offer no choice. Now it does, and the whole
// difficulty is that the choice is the cloud's to grant: a plan can lapse
// between two openings of this form. Three properties are tested here, and
// they are the three ways this feature could quietly betray somebody.

function modelSettingsBridge({ catalog, settings, onPut }) {
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-10T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") return response({ items: [] });
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const stored = {
    preferred_route: "official",
    official_model_id: "",
    local: { protocol: "", base_url: "", model_id: "", api_key_configured: false },
    updated_at: "2026-08-10T00:00:00Z",
    ...settings,
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      async listRecoverableTurns() { return typedSuccess({ items: [], count: 0 }); },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
    },
    settings: {
      async getModelRoute() { return typedSuccess(stored); },
      async putModelRoute(body) {
        if (onPut) onPut(body);
        Object.assign(stored, body);
        return typedSuccess(stored);
      },
      async getModelCatalog() { return typedSuccess(catalog); },
    },
  };
  return { bridge, desktopBridge, stored };
}

function catalogPayload(overrides = {}) {
  return {
    state: "ready",
    items: [
      {
        modelId: "work-pro", displayName: "WorkMax Pro", description: "Everyday",
        requiredTier: "free", permissions: ["use"], default: true,
      },
      {
        modelId: "work-plus", displayName: "WorkMax Plus", description: "Deeper",
        requiredTier: "pro", permissions: [], default: false,
      },
    ],
    count: 2,
    tier: "free",
    tier_expires_at: "",
    selected_model_id: "",
    selection_state: "unset",
    ...overrides,
  };
}

// A model the plan does not include is SHOWN and disabled, labelled with the
// tier that unlocks it. Hiding it would answer "what does upgrading buy me?"
// with silence.
async function testOfficialModelPickerShowsLockedModelsWithTheirTier() {
  const { bridge, desktopBridge } = modelSettingsBridge({ catalog: catalogPayload() });
  const { document } = await runRenderer(bridge, desktopBridge);
  await settle();

  document.byId.get("settings-button").click();
  await settle();

  const select = document.byId.get("model-official-id");
  const options = select.children;
  assert.equal(options[0].value, "", "the first option is the account default");
  assert.equal(options[1].value, "work-pro");
  assert.equal(options[1].disabled, false, "an included model is choosable");
  assert.match(options[1].textContent, /default/, "the account default is marked as such");
  assert.equal(options[2].value, "work-plus");
  assert.equal(options[2].disabled, true, "a model the plan excludes must not be choosable");
  assert.match(
    options[2].textContent,
    /needs pro/i,
    "and it must say which plan unlocks it — a greyed row with no reason teaches nothing",
  );
  assert.equal(
    document.byId.get("model-official-fields").hidden,
    false,
    "the official route shows its model picker",
  );
}

// The OSS-4 rule, one level down: a stored choice that stopped being allowed
// is a question for the user, never a silent swap for something that works.
async function testDowngradedModelSelectionIsSurfacedNotSubstituted() {
  const puts = [];
  const { bridge, desktopBridge } = modelSettingsBridge({
    settings: { official_model_id: "work-plus" },
    catalog: catalogPayload({ selected_model_id: "work-plus", selection_state: "not_allowed" }),
    onPut: (body) => puts.push(body),
  });
  const { document } = await runRenderer(bridge, desktopBridge);
  await settle();

  document.byId.get("settings-button").click();
  await settle();

  assert.equal(document.byId.get("model-settings-error").hidden, false);
  assert.match(
    document.byId.get("model-settings-error").textContent,
    /no longer includes|choose another/i,
    "an unusable stored choice must be stated, not quietly replaced",
  );
  assert.equal(
    document.byId.get("model-official-id").value,
    "work-plus",
    "the user's own choice stays on screen — swapping it would hide what happened",
  );

  // Saving without picking a new one is refused, so the disallowed choice
  // cannot be re-committed and fail later at send time.
  document.byId.get("model-settings-form").submit();
  await settle();
  assert.equal(puts.length, 0, "the disallowed selection must not be saved");
  assert.match(
    document.byId.get("model-settings-error").textContent,
    /not available on your plan/i,
  );

  // Picking an allowed one saves, and carries the model id to the sidecar.
  document.byId.get("model-official-id").value = "work-pro";
  document.byId.get("model-settings-form").submit();
  await settle();
  assert.equal(puts.length, 1);
  assert.equal(puts[0].official_model_id, "work-pro");
}

// Nothing to choose from is a state with a reason, not an empty dropdown.
async function testOfficialModelPickerExplainsItselfWithoutAnAccount() {
  const { bridge, desktopBridge } = modelSettingsBridge({
    catalog: catalogPayload({ state: "unbound", items: [], count: 0, tier: "" }),
  });
  const { document } = await runRenderer(bridge, desktopBridge);
  await settle();

  document.byId.get("settings-button").click();
  await settle();

  assert.match(
    document.byId.get("model-official-note").textContent,
    /connect an account/i,
    "an unbound desktop must say what would make models available",
  );
  assert.equal(
    document.byId.get("model-official-id").disabled,
    true,
    "and must not offer a choice it cannot honour",
  );
}

// --- Per-thread sync switch --------------------------------------------------
//
// "paused" was honoured by the sync writer and the history reader long before
// anything could write it. This is the write end, from the sidebar.
async function testThreadSyncSwitchPausesAndStaysVisible() {
  const calls = [];
  let state = "synced";
  const bridge = {
    async fetch(pathname) {
      if (pathname === "/auth/status") {
        return response({ state: "authenticated", updated_at: "2026-08-10T00:00:00Z" });
      }
      if (pathname === "/agent/threads?include_paused=true") {
        return response({
          items: [
            { ...thread("00000000-0000-4000-8000-0000000f1001", "Cloud deck"), cloud_sync_state: state },
            { ...thread("00000000-0000-4000-8000-0000000f1002", "Scratch"), cloud_sync_state: "local" },
          ],
        });
      }
      if (pathname.startsWith("/agent/threads/")) return response({ items: [] });
      throw new Error(`unexpected fetch path ${pathname}`);
    },
  };
  const desktopBridge = {
    agent: {
      async uploadThreadFile() { throw new Error("not exercised"); },
      async listSkills() { return typedSuccess(pptCatalog()); },
      async listRecoverableTurns() { return typedSuccess({ items: [], count: 0 }); },
      async cancelTurn(turnID) { return { turnID, canceled: true }; },
      async setThreadCloudSync(uuid, next) {
        calls.push([uuid, next]);
        state = next;
        return typedSuccess({ thread_uuid: uuid, cloud_sync_state: next });
      },
    },
  };
  const { document } = await runRenderer(bridge, desktopBridge);
  await settle();

  const syncButtons = () =>
    walk(document.byId.get("thread-list"), (node) =>
      node.classList?.contains("thread-sync")
    );

  const buttons = syncButtons();
  assert.equal(
    buttons.length,
    1,
    "only a thread the cloud already knows about gets the switch — a local-only thread has no sync to pause",
  );
  assert.equal(buttons[0].textContent, "Syncing");

  buttons[0].dispatch("click");
  await settle();
  assert.deepEqual(calls, [["00000000-0000-4000-8000-0000000f1001", "paused"]]);

  // Still in the sidebar, now labelled: pausing sync is not deleting.
  assert.match(
    document.byId.get("thread-list").textContent,
    /Cloud deck/,
    "a paused conversation must stay in the list — disappearing would read as deletion",
  );
  const afterButtons = syncButtons();
  assert.equal(afterButtons[0].textContent, "Local only");
  assert.equal(afterButtons[0].classList.contains("paused"), true);

  // And back again.
  afterButtons[0].dispatch("click");
  await settle();
  assert.deepEqual(calls.at(-1), ["00000000-0000-4000-8000-0000000f1001", "synced"]);
}

await testMissingBridge();
await testLongTranscriptMountsAWindowNotTheWholeHistory();
await testShortTranscriptHasNoWindowControl();
await testShowEarlierPrependsWithoutRebuildingWhatIsMounted();
await testShowEarlierHoldsTheScrollAnchorAndRetiresItselfAtTheTop();
await testCompletedTurnReconcilesInPlaceInsideTheWindow();
await testFencesInvalidateOnBumpAndCascadeFromSession();
await testAuthenticatedCacheRead();
await testUnauthenticatedLogin();
await testResumesAndCancelsPasswordLogin();
await testInvalidCredentialsStayRetryableAndClearPassword();
await testCancelFencesLatePasswordCompletion();
await testAmbiguousPasswordResponseReconcilesSessionWithoutReplay();
await testRejectsMalformedAuthStatus();
await testRejectsMalformedThreadList();
await testRejectsMalformedThreadCountAndTimestamp();
await testRejectsMalformedMessages();
await testRejectsMalformedMessageTimestamps();
await testRejectsMalformedLoginTransactionResult();
await testRedactsErrorStatusMessages();
await testCachedStreamingStatesRenderPartialAndRejectUnknown();
await testThreadGroupingAndSearch();
await testEmptyRailSaysNothingIsCached();
await testTaskContextPanelRendersOnLoad();
await testShimInterceptsExternalLinks();
await testThreadDeleteIsTwoStepAndLocalOnly();
await testThreadRenameFlow();
await testDropAndPasteAttachFiles();
await testMultiFileUploadCompletesEveryFile();
await testRejectedUploadFailsTheChipWithoutUnhandledRejection();
await testComposerDraftSurvivesThreadSwitch();
await testRegenerateRunsTheLastPromptAgain();
await testQuickSwitcherJumpsBetweenThreads();
await testEscapeStopsAStreamingTurn();
await testComposerCapacityNote();
await testStreamingDoesNotYankAScrolledUpReader();
await testStreamingDeltaCostsStayConstant();
await testNonDeltaEventsDrainBufferedText();
await testCompletedTurnReconcilesInPlace();
await testStreamingMarkdownCommitsMatchTheFinalParse();
await testTurnStatePillCarriesItsTone();
await testFailedTurnStateAndDuration();
await testModelProtocolHintFollowsTheChoice();
await testToolLoopActivityAndDeliverables();
await testToolApprovalCardsAndReasoningCaption();
await testThoughtSummaryReadsAsProse();
await testApprovalBecomesTheStepItIsAbout();
await testDenialFoldsIntoTheRowItAnswers();
await testStarterPromptLandsInTheComposer();
await testCancelledStarterDropsItsPrompt();
await testSelectedSourcesRideTheNextTurn();
await testMessageActionsCopyAndReuse();
await testMessageActionsAbsentWithoutAClipboard();
await testStreamedAnswerGainsActionsWhenReconcileFails();
await testSignedOutLocalRouteCanDriveTheAgent();
await testLocalAccountSwitcherSwitchesAndReloads();
await testSettingsSectionsAreOneAtATime();
await testModelSectionLoadsWhenReachedFromTheNav();
await testLocalAccountCreateDoesNotSwitch();
await testLocalAccountRenameIsALabelChange();
await testLocalAccountDeleteIsArmedAndScoped();
await testComposerChipsNameRuntimeAndIdentity();
await testComposerAccountChipSkipsTheDefaultIdentity();
await testExportThreadWritesAndReveals();
await testOfficialModelPickerShowsLockedModelsWithTheirTier();
await testDowngradedModelSelectionIsSurfacedNotSubstituted();
await testOfficialModelPickerExplainsItselfWithoutAnAccount();
await testThreadSyncSwitchPausesAndStaysVisible();
await testOnboardingPathsLeadSomewhere();
await testOnboardingHiddenOnceUsable();
await testBridgeLibAcceptsLocalCreateContract();
await testSignedOutLocalCanCreateThread();
await testPaletteSearchesMessageBodies();
await testPaletteRunsCommands();
await testPaletteOpensWithoutThreads();
await testColumnFolds();
await testSidebarSearchIconOpensThePalette();
await testPinnedThreadsLeadTheSidebar();
await testModesParseFailureNamesTheSkew();
await testLocalIdentityIsNamedWithoutAnyModel();
await testConnectedAccountIsShownAsABindingOnTheLocalIdentity();
await testSignedOutWithoutAModelKeepsTheWorkbench();
await testAssistantMarkdownIsRenderedAsElements();
await testAppearanceIsThreeStateAndPersisted();
await testDensityIsThreeStateAndPersisted();
await testCodeTokenizerClassifiesTheLanguagesWeShip();
await testCodeBlocksArePaintedWithoutMarkup();
await testRetrievedContextIsShownAndResetPerTurn();
await testShimValidatesRetrievalPayloads();
await testShimDropsMalformedApprovalAndReasoningFrames();
await testStagedAttachmentsAreSentWithTheTurn();
await testSynchronousTurnCallbacksAreBufferedUntilOpenResult();
await testAgentTurnStreamsAndReconciles();
await testAnswersKeepNamingTheirOwnOrigin();
await testLateThreadHistoryCannotContaminateSelection();
await testThreadSwitchCancelsAndFencesOldTurn();
await testStopTurnIsSingleShot();
await testInitialTurnBusyRefreshesRecoveryWithoutReplay();
await testCancelAckFailureShowsLocalStopAndRefreshesRecovery();
await testSSESessionChangedClearsPromptWithoutReplay();
await testCatalog409UsesSessionChangedRecovery();
await testRejectsMalformedAgentContractsWithoutLeakingPayload();
await testRejectsLegacyOpenAgentEventShapes();
await testRejectsMalformedCatalogResult();
await testRecoverableTurnRequiresExplicitResumeAndHandlesBusy();
await testRecoverableTurnDismissIsExplicitAndIdempotent();
await testRecoverableErrorResultIsSanitized();
await testMalformedRecoverableTurnDoesNotLeakOrRender();
await testCreatesThreadOnceAndFocusesComposer();
await testCreateRetriesKeepUUIDAndAcceptCurrentReplayRow();
await testPermanentCreateFailuresDoNotOfferSameIdentityRetry();
await testPausedCreateReplayRequiresExplicitCancel();
await testCreateEscapeFencesLateCompletion();
await testCreateSessionChangedUsesUnifiedRecovery();
// --- The mind (心智体) --------------------------------------------------------
//
// The panel's whole claim is that what it shows is real: the model comes from
// the sidecar's status, the passage counts come from the knowledge store, and
// the icon moves only when something mental actually happened. Each of those
// is a way for a panel like this to start lying quietly, so each one is
// pinned here.

const MIND_A = "mind-de305d54-75b4-431b-adb2-eb6b9e546014";
const MIND_B = "mind-11111111-1111-4111-8111-111111111111";

function mindRecord(id, name, overrides = {}) {
  return {
    id,
    name,
    description: "",
    role_hint: "",
    model_override: "",
    active: false,
    created_at: "2026-08-12T00:00:00Z",
    updated_at: "2026-08-12T00:00:00Z",
    ...overrides,
  };
}

function mindStatusRecord(mind, overrides = {}) {
  return {
    mind,
    model: { source: "identity", label: "claude-sonnet-4.6", route: "official" },
    retrieval: "local",
    memory: { chunks: 0, sources: [] },
    ...overrides,
  };
}

// A sidecar that really stores what it is taught: feeding appends to the
// mind's memory and the next status read reflects it, which is the only way
// the "did the count move" assertion means anything.
function mindBridge({ minds, retrieval = "local", model } = {}) {
  const roster = minds ?? [
    mindRecord(MIND_A, "General mind", { active: true, role_hint: "The everyday one" }),
    mindRecord(MIND_B, "Payroll mind", { model_override: "claude-opus-4.1" }),
  ];
  const memory = new Map(roster.map((mind) => [mind.id, []]));
  const calls = { list: 0, status: [], selected: [], created: [], fed: [] };
  return {
    calls,
    roster,
    // The signed-out local route, which is the state a mind is most likely to
    // be trained in: the knowledge base is on this machine and no account is
    // connected to it.
    bridge: {
      async fetch(pathname) {
        if (pathname === "/auth/status") {
          return response({ state: "unauthenticated", updated_at: "2026-08-12T00:00:00Z" });
        }
        if (pathname === "/agent/threads?include_paused=true") {
          return response({ items: [] });
        }
        throw new Error(`unexpected fetch path ${pathname}`);
      },
    },
    desktopBridge: {
      agent: {
        async listSkills() {
          return typedSuccess(pptCatalog());
        },
        async listModes() {
          return typedSuccess({ allowed_modes: ["ppt"], local_route: true, tool_loop: false });
        },
        async listRecoverableTurns() {
          return typedSuccess({ items: [], count: 0 });
        },
        async uploadThreadFile() {
          throw new Error("not exercised");
        },
        async createThread() {
          throw new Error("not exercised");
        },
        async resumeTurn() {
          throw new Error("not exercised");
        },
        startTurn() {
          return { turnID: "mind-turn" };
        },
        async cancelTurn(turnID) {
          return { turnID, canceled: true };
        },
      },
      mind: {
        async list() {
          calls.list += 1;
          return typedSuccess({ items: roster.slice(), count: roster.length });
        },
        async status(id) {
          calls.status.push(id);
          const mind = roster.find((entry) => entry.id === id);
          const sources = memory.get(id) ?? [];
          return typedSuccess(
            mindStatusRecord(mind, {
              retrieval,
              model: model ?? {
                source: mind.model_override ? "mind" : "identity",
                label: mind.model_override || "claude-sonnet-4.6",
                route: "official",
              },
              memory: {
                chunks: sources.reduce((total, source) => total + source.chunks, 0),
                sources: sources.slice(),
              },
            }),
          );
        },
        async select(id) {
          calls.selected.push(id);
          for (const mind of roster) mind.active = mind.id === id;
          return typedSuccess({ selected: true });
        },
        async create(input) {
          calls.created.push(input.name);
          const created = mindRecord(MIND_B, input.name);
          roster.push(created);
          memory.set(created.id, []);
          return typedSuccess(created, 201);
        },
        async feed(id, input) {
          calls.fed.push([id, input.title]);
          const sources = memory.get(id) ?? [];
          const chunks = 3;
          memory.set(id, [
            ...sources.filter((source) => source.title !== input.title),
            { title: input.title, chunks, indexed_at: 1_770_000_000 },
          ]);
          return typedSuccess({ fed: true, title: input.title, chunks });
        },
      },
    },
  };
}

async function testMindPanelShowsRealAnatomyAndTeaches() {
  const harness = mindBridge();
  const { document } = await runRenderer(harness.bridge, harness.desktopBridge);
  await settle();

  // Not in the column until asked for. The mind is a long-lived thing, not a
  // panel that greets you — and the right column's default occupant is the
  // workspace ledger.
  assert.equal(document.documentElement.getAttribute("data-right-panel"), null);

  document.byId.get("mind-button").click();
  await settle();
  assert.equal(
    document.documentElement.getAttribute("data-right-panel"),
    "mind",
    "the icon puts the mind in the right column",
  );
  assert.ok(harness.calls.list > 0, "opening reads the roster from the sidecar");
  assert.deepEqual(harness.calls.status, [MIND_A], "and the active mind's real status");

  // Both minds are listed, and the active one says so.
  const rosterRows = walk(
    document.byId.get("mind-roster"),
    (node) => node.classList?.contains("mind-roster-item"),
  );
  assert.equal(rosterRows.length, 2, "every mind on this identity is switchable");
  assert.ok(rosterRows[0].classList.contains("active"), "the active mind is marked");

  // The three parts carry the sidecar's answers, not invented ones.
  assert.equal(
    document.byId.get("mind-brain-value").textContent,
    "claude-sonnet-4.6",
    "the brain names the model the sidecar reported",
  );
  assert.match(
    document.byId.get("mind-brain-note").textContent,
    /From your account/u,
    "and says the model came from the identity rather than from this mind",
  );
  assert.match(
    document.byId.get("mind-skills-value").textContent,
    /1 skill/u,
    "the cerebellum counts the skills this workspace really has",
  );
  assert.equal(
    document.byId.get("mind-memory-value").textContent,
    "0 passages",
    "an untaught mind must say it holds nothing",
  );
  assert.equal(
    document.byId.get("mind-memory-section").hidden,
    true,
    "and the listing stands down rather than drawing an empty scaffold",
  );

  // Teaching it: the material goes to the sidecar and the count comes back
  // from the sidecar's own re-read, never from an optimistic increment.
  document.byId.get("mind-feed-title").value = "Compensation bands";
  document.byId.get("mind-feed-text").value = "L4 starts at 180k base.";
  document.byId.get("mind-feed-form").submit();
  await settle();

  assert.deepEqual(harness.calls.fed, [[MIND_A, "Compensation bands"]]);
  assert.equal(
    document.byId.get("mind-memory-value").textContent,
    "3 passages",
    "the memory count moves to what the store actually indexed",
  );
  assert.equal(document.byId.get("mind-memory-section").hidden, false);
  const memoryRows = walk(
    document.byId.get("mind-memory-list"),
    (node) => node.classList?.contains("mind-memory-item"),
  );
  assert.equal(memoryRows.length, 1);
  assert.match(memoryRows[0].textContent, /Compensation bands/u);
  assert.match(memoryRows[0].textContent, /3 passages/u);
  assert.match(
    document.byId.get("mind-feed-status").textContent,
    /Learned "Compensation bands" as 3 passages/u,
    "the form says what it taught, in the store's units",
  );
  assert.equal(document.byId.get("mind-feed-title").value, "", "a taught material clears its form");

  // A half-written material survives the column being folded away and brought
  // back. The panel used to be an overlay you dismissed on purpose; it is a
  // column now, folded with the same title-bar icon that shows it, and a panel
  // that eats a draft when it is folded is a panel nobody folds twice.
  document.byId.get("mind-feed-title").value = "Half a thought";
  document.byId.get("mind-button").click();
  await settle();
  document.byId.get("mind-button").click();
  await settle();
  assert.equal(
    document.byId.get("mind-feed-title").value,
    "Half a thought",
    "folding the column away must not throw away what was being typed into it",
  );
}

// --- The composer names the mind that will answer ----------------------------
//
// A mind decides which memory is in scope, which model reads it, and how that
// model works. Finding that out from the transcript afterwards is finding it
// out too late; the panel shows it, but only to someone who opened the panel.
//
// What is worth testing is WHEN it appears. A chip that says the same word on
// every message teaches the reader to stop reading it, which costs the times
// it would have mattered — so it stands down when there is nothing it could
// distinguish, exactly as the account chip beside it does for a placeholder
// name.
async function testComposerNamesTheMindThatWillAnswer() {
  // One mind, asking nothing of the turn: nothing to distinguish, no chip.
  const quiet = mindBridge({
    minds: [mindRecord(MIND_A, "General mind", { active: true })],
  });
  const bare = await runRenderer(quiet.bridge, quiet.desktopBridge);
  await settle();
  assert.equal(
    bare.document.byId.get("mind-chip").hidden,
    true,
    "a lone mind with no opinion has nothing to say on every message",
  );

  // One mind that DOES govern the turn — it names a model of its own — is
  // worth saying, because the answer will differ from the identity's setting.
  const opinionated = mindBridge({
    minds: [mindRecord(MIND_A, "Payroll mind", { active: true, model_override: "claude-opus-4.1" })],
  });
  const single = await runRenderer(opinionated.bridge, opinionated.desktopBridge);
  await settle();
  const soloChip = single.document.byId.get("mind-chip");
  assert.equal(soloChip.hidden, false, "a mind that changes the turn names itself");
  assert.equal(soloChip.textContent, "Payroll mind");

  // Two minds: which one is active is now a real question whichever way it is
  // answered, so the chip is always on.
  const harness = mindBridge();
  const { document } = await runRenderer(harness.bridge, harness.desktopBridge);
  await settle();
  const chip = document.byId.get("mind-chip");
  assert.equal(chip.hidden, false);
  assert.equal(chip.textContent, "General mind");
  assert.match(
    chip.getAttribute("aria-label"),
    /^Mind: General mind\./u,
    "a chip that is a button has to say what pressing it is about",
  );

  // Switching repaints it. The roster is the only thing that knows the active
  // mind changed, so the composer has to be told rather than to poll.
  document.byId.get("mind-button").click();
  await settle();
  walk(document.byId.get("mind-roster"), (n) => n.classList?.contains("mind-roster-item"))[1].click();
  await settle();
  assert.equal(chip.textContent, "Payroll mind", "the composer follows the switch");

  // And it is the way in: a chip that states a governing fact and offers no
  // way to act on it makes the reader hunt for the control.
  document.byId.get("mind-button").click();
  await settle();
  assert.equal(document.documentElement.getAttribute("data-right-panel"), null);
  chip.click();
  await settle();
  assert.equal(
    document.documentElement.getAttribute("data-right-panel"),
    "mind",
    "pressing the chip opens the panel that changes what it names",
  );
}

async function testMindSwitchingReReadsTheStatus() {
  const harness = mindBridge();
  const { document } = await runRenderer(harness.bridge, harness.desktopBridge);
  await settle();
  document.byId.get("mind-button").click();
  await settle();

  const rows = () =>
    walk(document.byId.get("mind-roster"), (node) => node.classList?.contains("mind-roster-item"));
  rows()[1].click();
  await settle();

  assert.deepEqual(harness.calls.selected, [MIND_B], "clicking a mind switches to it");
  assert.equal(
    harness.calls.status.at(-1),
    MIND_B,
    "and the panel re-reads the status of the mind it switched to",
  );
  assert.equal(
    document.byId.get("mind-brain-value").textContent,
    "claude-opus-4.1",
    "a mind with its own model shows that model, not the identity's",
  );
  assert.match(
    document.byId.get("mind-brain-note").textContent,
    /Chosen for this mind/u,
    "and says whose choice it was",
  );

  // Creating one is a switchable addition, not a takeover.
  document.byId.get("mind-create-name").value = "Research mind";
  document.byId.get("mind-create-form").submit();
  await settle();
  assert.deepEqual(harness.calls.created, ["Research mind"]);
  assert.equal(document.byId.get("mind-create-name").value, "");
}

async function testMindIconMovesOnlyForRealMentalActivity() {
  const harness = mindBridge();
  const { document, ns } = await runRenderer(harness.bridge, harness.desktopBridge);
  await settle();
  const button = document.byId.get("mind-button");

  // Idle: no attribute at all, so the stylesheet runs no animation. An icon
  // that breathes on a timer would be decoration pretending to be information.
  assert.equal(
    button.getAttribute("data-mind-activity"),
    null,
    "an idle mind icon must be perfectly still",
  );

  // Reasoning tokens are real thinking.
  ns.noteMindActivity("thinking");
  assert.equal(button.getAttribute("data-mind-activity"), "thinking");
  // Teaching is a different, distinguishable meaning.
  ns.noteMindActivity("learning");
  assert.equal(button.getAttribute("data-mind-activity"), "learning");
  // Anything that is not one of the two real events moves nothing.
  ns.noteMindActivity("idle-decoration");
  assert.equal(
    button.getAttribute("data-mind-activity"),
    "learning",
    "an unrecognised cue must not become motion",
  );

  // And it puts itself down: the VM maps setTimeout to setImmediate, so the
  // decay lands on the next tick rather than seconds later.
  await settle();
  assert.equal(
    button.getAttribute("data-mind-activity"),
    null,
    "activity must expire on its own, or the icon becomes furniture",
  );

  // The stylesheet's half of the same contract: motion exists only under the
  // attribute, and reduced motion still leaves the state readable.
  assert.match(
    rendererCSS,
    /\.mind-button\[data-mind-activity="thinking"\] svg \.mind-button-core \{\s*animation: mind-breathe/u,
    "thinking must be the only reason the core breathes",
  );
  assert.match(
    rendererCSS,
    /@media \(prefers-reduced-motion: reduce\) \{\s*\.mind-button\[data-mind-activity\] svg \.mind-button-core \{\s*animation: none;/u,
    "reduced motion must drop the animation without dropping the state",
  );
  assert.doesNotMatch(
    rendererCSS,
    /\.mind-button svg \.mind-button-core \{[^}]*animation:/u,
    "the resting icon must declare no animation at all",
  );
  // Colour is the half of the cue that survives reduced motion, and it has to
  // out-specify button.ghost's resting grey. Written as `.mind-button[…]` it
  // measured identical to idle in a real engine — an icon that cannot say it
  // is working. The element-qualified selector is the fix, so it is pinned.
  assert.match(
    rendererCSS,
    /button\.mind-button\[data-mind-activity\] \{\s*color: hsl\(var\(--primary\)\);/u,
    "the active icon must out-specify button.ghost, or the state is invisible",
  );
}

async function testMindFeedIsRefusedWithoutLocalRecall() {
  const harness = mindBridge({ retrieval: "unavailable" });
  const { document } = await runRenderer(harness.bridge, harness.desktopBridge);
  await settle();
  document.byId.get("mind-button").click();
  await settle();

  // A build with no local recall can still name and switch minds; what it
  // cannot do is pretend a material was learned. Said as a disabled control
  // plus a reason, not as a silent no-op.
  assert.equal(
    document.byId.get("mind-feed-button").disabled,
    true,
    "teaching must be refused where nothing could recall it",
  );
  assert.match(
    document.byId.get("mind-memory-note").textContent,
    /Local recall is unavailable/u,
  );
  assert.equal(
    walk(document.byId.get("mind-roster"), (node) => node.classList?.contains("mind-roster-item"))
      .length,
    2,
    "the roster still works without the knowledge stack",
  );
}

// Registered here rather than beside the other fold tests: it drives the mind
// bridge, whose fixtures are declared in this section.
await testRightColumnHoldsOnePanelAtATime();
await testMindPanelShowsRealAnatomyAndTeaches();
await testComposerNamesTheMindThatWillAnswer();
await testMindSwitchingReReadsTheStatus();
await testMindIconMovesOnlyForRealMentalActivity();
await testMindFeedIsRefusedWithoutLocalRecall();

console.log("ok bundled renderer behavior");
