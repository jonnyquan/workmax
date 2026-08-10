// Markdown and code highlighting: model output to DOM nodes.
//
// Self-contained on purpose. It builds every node with createElement and
// createTextNode and touches no application state, which is what makes "model
// output cannot become markup" a property of this file rather than a promise
// about escaping — and which is why the file is worth being able to read on
// its own.
// Models answer in Markdown. Until this existed the renderer put that string
// straight into textContent, so every list arrived as "- item", every code
// block as three backticks, and every heading as "###". The content was right
// and unreadable.
//
// Everything below builds DOM nodes. There is no innerHTML on this path and no
// HTML parsing of model output at any point: a document fragment assembled
// from createElement and createTextNode cannot execute anything, whatever the
// model was talked into writing. That is the whole security argument, and it
// is structural rather than a matter of escaping correctly.

// Above this the text is rendered plain. A wall this large is not made
// readable by formatting, and it keeps a single parse bounded.
export const MARKDOWN_MAX_CHARS = 200_000;

export const MARKDOWN_FENCE = /^\s{0,3}(`{3,}|~{3,})\s*([A-Za-z0-9_+-]*)\s*$/u;
const MARKDOWN_HEADING = /^\s{0,3}(#{1,6})\s+(.*)$/u;
const MARKDOWN_RULE = /^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/u;
const MARKDOWN_BULLET = /^(\s*)[-*+]\s+(.*)$/u;
const MARKDOWN_ORDERED = /^(\s*)(\d{1,9})[.)]\s+(.*)$/u;
const MARKDOWN_QUOTE = /^\s{0,3}>\s?(.*)$/u;
const INLINE_ESCAPABLE = "\\`*_~[]()#-";

// The class goes on here rather than at bubble creation because it is what
// switches the bubble from pre-wrap (right for the raw text arriving during a
// stream) to normal flow (right once blocks own their own spacing). Setting it
// early would collapse the newlines of a streaming answer.
export function renderMarkdownInto(container, text) {
  container.textContent = "";
  container.classList.remove("markdown");
  if (typeof text !== "string" || text === "") return;
  if (text.length > MARKDOWN_MAX_CHARS) {
    container.textContent = text;
    return;
  }
  container.classList.add("markdown");
  for (const block of parseMarkdownBlocks(text.split(/\r\n|\r|\n/u))) {
    container.appendChild(block);
  }
}

export function parseMarkdownBlocks(lines) {
  const blocks = [];
  let index = 0;
  while (index < lines.length) {
    const line = lines[index];
    if (line.trim() === "") {
      index += 1;
      continue;
    }

    const fence = MARKDOWN_FENCE.exec(line);
    if (fence) {
      const [, marker, language] = fence;
      const body = [];
      index += 1;
      // An unterminated fence runs to the end rather than falling back to
      // paragraphs: while a turn is still streaming that is the normal state,
      // and reflowing it as prose would make the block flicker between two
      // shapes as the closing fence arrives.
      while (index < lines.length && !isClosingFence(lines[index], marker)) {
        body.push(lines[index]);
        index += 1;
      }
      if (index < lines.length) index += 1;
      blocks.push(buildCodeBlock(body.join("\n"), language));
      continue;
    }

    const heading = MARKDOWN_HEADING.exec(line);
    if (heading) {
      // Clamped to h4-h6 so a model-authored "#" can never outrank the
      // application's own headings in the document outline.
      const level = Math.min(6, heading[1].length + 3);
      const node = document.createElement(`h${level}`);
      node.className = "md-heading";
      parseInlineInto(node, heading[2].trim());
      blocks.push(node);
      index += 1;
      continue;
    }

    if (MARKDOWN_RULE.test(line)) {
      blocks.push(document.createElement("hr"));
      index += 1;
      continue;
    }

    if (MARKDOWN_QUOTE.test(line)) {
      const quoted = [];
      while (index < lines.length && MARKDOWN_QUOTE.test(lines[index])) {
        quoted.push(MARKDOWN_QUOTE.exec(lines[index])[1]);
        index += 1;
      }
      const node = document.createElement("blockquote");
      for (const child of parseMarkdownBlocks(quoted)) node.appendChild(child);
      blocks.push(node);
      continue;
    }

    const listStart = matchListItem(line);
    if (listStart) {
      const node = document.createElement(listStart.ordered ? "ol" : "ul");
      node.className = "md-list";
      while (index < lines.length) {
        const item = matchListItem(lines[index]);
        if (!item || item.ordered !== listStart.ordered) break;
        const li = document.createElement("li");
        parseInlineInto(li, item.text);
        node.appendChild(li);
        index += 1;
      }
      blocks.push(node);
      continue;
    }

    // A paragraph runs until a blank line or the start of another block.
    const paragraph = [];
    while (index < lines.length && lines[index].trim() !== "" && !startsBlock(lines[index])) {
      paragraph.push(lines[index].trim());
      index += 1;
    }
    const node = document.createElement("p");
    node.className = "md-paragraph";
    parseInlineInto(node, paragraph.join("\n"));
    blocks.push(node);
  }
  return blocks;
}

export function isClosingFence(line, marker) {
  const trimmed = line.trim();
  return trimmed.startsWith(marker[0].repeat(3)) && trimmed === marker[0].repeat(trimmed.length);
}

function startsBlock(line) {
  return (
    MARKDOWN_FENCE.test(line) ||
    MARKDOWN_HEADING.test(line) ||
    MARKDOWN_RULE.test(line) ||
    MARKDOWN_QUOTE.test(line) ||
    matchListItem(line) !== null
  );
}

function matchListItem(line) {
  const ordered = MARKDOWN_ORDERED.exec(line);
  if (ordered) return { ordered: true, text: ordered[3] };
  const bullet = MARKDOWN_BULLET.exec(line);
  // A rule ("---") also matches the bullet pattern; rules are checked first by
  // the caller, but matchListItem is used for lookahead too.
  if (bullet && !MARKDOWN_RULE.test(line)) return { ordered: false, text: bullet[2] };
  return null;
}

function buildCodeBlock(code, language) {
  const pre = document.createElement("pre");
  pre.className = "md-code";
  const node = document.createElement("code");
  if (language) {
    // Recorded for styling only; nothing reads it back to decide behaviour, so
    // an unknown language is inert rather than something to validate against a
    // list that would go stale.
    node.className = `language-${language.toLowerCase()}`;
  }
  // Plain text first, always. Highlighting is a later, optional improvement on
  // something already readable — see scheduleCodeHighlight.
  node.textContent = code;
  scheduleCodeHighlight(node, code, language);
  pre.appendChild(node);

  // A code block the user cannot get out of the window is a screenshot of
  // code. The button is a sibling of <pre>, not a child: inside it, its label
  // would become part of the block's own text — so selecting the code by hand,
  // or copying it, would pick up the word "Copy".
  // The language rides in the fence ("```sql") and until now was recorded
  // only as a class. Naming it answers "what am I looking at" before the
  // reader parses a line.
  const tag = language
    ? (() => {
        const badge = document.createElement("span");
        badge.className = "md-code-lang";
        badge.textContent = language.toLowerCase();
        return badge;
      })()
    : null;
  const copy = buildCopyButton(code, "Copy code");
  if (!copy && !tag) return pre;
  // A header strip, the shape every reader already knows from code hosts:
  // what it is on the left, what you can do with it on the right.
  const wrap = document.createElement("div");
  wrap.className = "md-code-wrap";
  const head = document.createElement("div");
  head.className = "md-code-head";
  head.appendChild(tag || document.createElement("span"));
  if (copy) head.appendChild(copy);
  wrap.append(head, pre);
  return wrap;
}

// --- Syntax highlighting -----------------------------------------------------
//
// Self-contained on purpose. Every off-the-shelf highlighter is either a
// megabyte of grammars or wants to hand back HTML, and both are the wrong
// shape here: the shell serves an allowlist of five renderer files under
// script-src 'self', so a new dependency is a new shipped file to justify, and
// anything returning markup would have to be written with innerHTML — the one
// thing the whole Markdown renderer above exists to avoid. So: a scanner that
// returns typed runs of TEXT, painted with createElement and createTextNode,
// structurally unable to inject anything whatever the model wrote.
//
// It is a display aid, not a compiler. It does not track scope, does not parse
// JS regex literals, and cannot tell a Python decorator from a matrix product.
// Being wrong colours a word; being slow or unsafe would cost much more.
//
// Coverage is what a coding agent actually emits: js/ts, python, go, json,
// sql, bash, html, css, markdown. Anything else keeps its plain text and its
// language badge, which is already most of the value.

// Bounds, in the same spirit as MARKDOWN_MAX_CHARS: past these a code block is
// a data dump, not something anyone reads, and colouring it would cost far
// more than it returns. Plain text is the honest fallback, not a failure.
const CODE_HIGHLIGHT_MAX_CHARS = 100_000;
const CODE_HIGHLIGHT_MAX_LINES = 5_000;
// A hard ceiling on DOM nodes for one block. Minified or highly punctuated
// input can produce a token per character; 6000 spans is already more than a
// screen can show, and past it the block goes back to being one text node.
const CODE_HIGHLIGHT_MAX_TOKENS = 6_000;

// Highlighting waits for idle time. The block is on screen as plain text the
// moment it is built — during a stream that is every few frames — and the
// colouring lands afterwards, so a long answer never pays for typesetting on
// the frame that shows it. requestIdleCallback is not everywhere (WebKit was
// late to it), and the behaviour suite's VM has neither it nor rAF, so a
// zero-delay timeout is the fallback: still after the current task, still
// before the suite's next settle().
const scheduleIdleWork =
  typeof globalThis.requestIdleCallback === "function"
    ? (callback) => {
        globalThis.requestIdleCallback(callback, { timeout: 500 });
      }
    : (callback) => {
        setTimeout(callback, 0);
      };

// Fence labels as people actually write them, mapped onto the grammars that
// exist. An unknown label resolves to nothing and the block stays plain.
const CODE_LANGUAGE_ALIASES = {
  js: "js",
  jsx: "js",
  javascript: "js",
  mjs: "js",
  cjs: "js",
  node: "js",
  ts: "js",
  tsx: "js",
  typescript: "js",
  py: "python",
  py3: "python",
  python: "python",
  python3: "python",
  go: "go",
  golang: "go",
  json: "json",
  json5: "json",
  jsonc: "json",
  sql: "sql",
  psql: "sql",
  mysql: "sql",
  sqlite: "sql",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  shell: "bash",
  console: "bash",
  html: "html",
  xml: "html",
  svg: "html",
  vue: "html",
  css: "css",
  scss: "css",
  less: "css",
  md: "markdown",
  markdown: "markdown",
};

// Sticky (`y`) so a rule can only match AT the cursor, never by searching
// ahead: the scanner's whole correctness argument is that position advances by
// exactly the text consumed. Unicode (`u`) for the same reason every other
// regex in this file carries it — and it is why a backtick in a pattern is
// written \x60 below: under `u` a backslash-backtick is an invalid escape.
function codeRule(type, source, extraFlags = "") {
  return { type, re: new RegExp(source, "yu" + extraFlags) };
}

// Fragments shared by the C-family grammars. Strings stop at a newline so an
// unbalanced quote colours one line rather than swallowing the rest of the
// file — the failure mode that makes naive highlighters look broken.
const CODE_DQ_STRING = String.raw`"(?:\\.|[^"\\\n])*"`;
const CODE_SQ_STRING = String.raw`'(?:\\.|[^'\\\n])*'`;
const CODE_LINE_COMMENT_SLASH = String.raw`//[^\n]*`;
const CODE_BLOCK_COMMENT = String.raw`/\*[\s\S]*?(?:\*/|$)`;
const CODE_NUMBER = String.raw`0[xXbBoO][0-9a-fA-F_]+n?|\d[\d_]*(?:\.[\d_]*)?(?:[eE][+-]?\d+)?n?`;
const CODE_WORD = String.raw`[A-Za-z_$][A-Za-z0-9_$]*`;
const CODE_PUNCT = String.raw`[{}()[\];,.:?=+\-*/%<>!&|^~@#]+`;

function words(list) {
  return new Set(list.split(" "));
}

// Each grammar is an ordered rule list plus, for languages that have them, the
// word sets. Order matters: comments before punctuation, strings before
// everything, so a `//` inside a string cannot start a comment.
const CODE_GRAMMARS = {
  js: {
    rules: [
      codeRule("comment", CODE_LINE_COMMENT_SLASH),
      codeRule("comment", CODE_BLOCK_COMMENT),
      codeRule("string", CODE_DQ_STRING),
      codeRule("string", CODE_SQ_STRING),
      codeRule("string", String.raw`\x60(?:\\.|[^\x60\\])*\x60`),
      codeRule("number", CODE_NUMBER),
      codeRule("word", CODE_WORD),
      codeRule("punct", CODE_PUNCT),
    ],
    keywords: words(
      "const let var function return if else for while do break continue new class extends " +
        "super this typeof instanceof in of delete void null undefined true false async await " +
        "yield try catch finally throw switch case default import export from as static get set " +
        "debugger interface type enum implements private public protected readonly abstract " +
        "declare namespace satisfies keyof infer"
    ),
    builtins: words(
      "Math JSON Object Array String Number Boolean Promise Symbol Map Set WeakMap WeakSet " +
        "Date RegExp Error TypeError document window globalThis fetch Headers URL " +
        "string number boolean any unknown never void object"
    ),
  },
  python: {
    rules: [
      codeRule("comment", String.raw`#[^\n]*`),
      codeRule("string", String.raw`[rRbBuUfF]{0,2}"""[\s\S]*?(?:"""|$)`),
      codeRule("string", String.raw`[rRbBuUfF]{0,2}'''[\s\S]*?(?:'''|$)`),
      codeRule("string", String.raw`[rRbBuUfF]{0,2}` + CODE_DQ_STRING),
      codeRule("string", String.raw`[rRbBuUfF]{0,2}` + CODE_SQ_STRING),
      codeRule("number", CODE_NUMBER),
      codeRule("word", CODE_WORD),
      codeRule("punct", CODE_PUNCT),
    ],
    keywords: words(
      "def class return if elif else for while break continue pass import from as with try " +
        "except finally raise lambda yield global nonlocal assert del in is not and or None " +
        "True False async await match case"
    ),
    builtins: words(
      "print len range str int float list dict set tuple bool open enumerate zip map filter " +
        "sum min max sorted type isinstance super self cls Exception ValueError TypeError"
    ),
  },
  go: {
    rules: [
      codeRule("comment", CODE_LINE_COMMENT_SLASH),
      codeRule("comment", CODE_BLOCK_COMMENT),
      codeRule("string", String.raw`\x60[^\x60]*\x60`),
      codeRule("string", CODE_DQ_STRING),
      codeRule("string", CODE_SQ_STRING),
      codeRule("number", CODE_NUMBER),
      codeRule("word", CODE_WORD),
      codeRule("punct", CODE_PUNCT),
    ],
    keywords: words(
      "package import func var const type struct interface map chan go defer if else for range " +
        "return switch case default break continue fallthrough goto select nil true false"
    ),
    builtins: words(
      "string bool byte rune error int int8 int16 int32 int64 uint uint8 uint16 uint32 uint64 " +
        "uintptr float32 float64 complex64 complex128 any len cap make new append copy delete " +
        "panic recover close min max"
    ),
  },
  json: {
    rules: [
      // A key is a string that a colon follows. Looking ahead is safe: the
      // lookahead is not consumed, so the cursor still advances by the string.
      codeRule("attr", String.raw`"(?:\\.|[^"\\])*"(?=\s*:)`),
      codeRule("string", String.raw`"(?:\\.|[^"\\])*"`),
      codeRule("number", String.raw`-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?`),
      codeRule("word", String.raw`[A-Za-z_][A-Za-z0-9_]*`),
      codeRule("punct", String.raw`[{}[\],:]`),
    ],
    keywords: words("true false null"),
    builtins: new Set(),
  },
  sql: {
    rules: [
      codeRule("comment", String.raw`--[^\n]*`),
      codeRule("comment", CODE_BLOCK_COMMENT),
      // SQL escapes a quote by doubling it, not with a backslash.
      codeRule("string", String.raw`'(?:''|[^'\n])*'`),
      codeRule("attr", String.raw`"[^"\n]*"`),
      codeRule("attr", String.raw`\x60[^\x60\n]*\x60`),
      codeRule("number", String.raw`\d+(?:\.\d+)?`),
      codeRule("word", String.raw`[A-Za-z_][A-Za-z0-9_]*`),
      codeRule("punct", String.raw`[(),;.*=<>+\-/%|]+`),
    ],
    // SQL keywords are case-insensitive and people write them both ways.
    foldWords: true,
    keywords: words(
      "SELECT FROM WHERE INSERT INTO VALUES UPDATE SET DELETE CREATE TABLE ALTER DROP INDEX " +
        "VIEW JOIN LEFT RIGHT INNER OUTER FULL CROSS ON AS AND OR NOT NULL IS IN EXISTS BETWEEN " +
        "LIKE GROUP BY ORDER HAVING LIMIT OFFSET UNION ALL DISTINCT CASE WHEN THEN ELSE END " +
        "PRIMARY KEY FOREIGN REFERENCES DEFAULT UNIQUE CONSTRAINT WITH RETURNING ASC DESC " +
        "BEGIN COMMIT ROLLBACK TRANSACTION IF CASCADE USING"
    ),
    builtins: words(
      "COUNT SUM AVG MIN MAX COALESCE NULLIF CAST NOW DATE TIMESTAMP INTEGER TEXT VARCHAR " +
        "BOOLEAN SERIAL BIGINT REAL BLOB JSON JSONB UUID"
    ),
  },
  bash: {
    rules: [
      codeRule("comment", String.raw`#[^\n]*`),
      codeRule("string", String.raw`"(?:\\.|[^"\\])*"`),
      codeRule("string", String.raw`'[^']*'`),
      codeRule("builtin", String.raw`\$\{[^}\n]*\}|\$[A-Za-z_][A-Za-z0-9_]*|\$[0-9@*#?$!]`),
      codeRule("number", String.raw`\d+`),
      codeRule("word", String.raw`[A-Za-z_][A-Za-z0-9_-]*`),
      codeRule("punct", String.raw`[|&;()<>=]+`),
    ],
    keywords: words(
      "if then else elif fi for while until do done case esac function in return exit local " +
        "export set unset source declare readonly shift trap break continue eval exec"
    ),
    builtins: words(
      "echo printf read cd ls cat grep sed awk cut sort uniq head tail find xargs cp mv rm " +
        "mkdir rmdir touch chmod chown ln pwd which curl wget tar zip unzip git make go node " +
        "npm pnpm yarn python python3 pip docker kubectl ssh scp sudo env test"
    ),
  },
  html: {
    rules: [
      codeRule("comment", String.raw`<!--[\s\S]*?(?:-->|$)`),
      codeRule("keyword", String.raw`<!DOCTYPE[^>\n]*>`, "i"),
      codeRule("tag", String.raw`</?[A-Za-z][A-Za-z0-9:-]*`),
      codeRule("string", String.raw`"[^"\n]*"`),
      codeRule("string", String.raw`'[^'\n]*'`),
      codeRule("attr", String.raw`[A-Za-z_:][A-Za-z0-9_:.-]*(?=\s*=)`),
      codeRule("punct", String.raw`[<>/=]+`),
    ],
    keywords: new Set(),
    builtins: new Set(),
  },
  css: {
    rules: [
      codeRule("comment", CODE_BLOCK_COMMENT),
      codeRule("string", CODE_DQ_STRING),
      codeRule("string", CODE_SQ_STRING),
      codeRule("keyword", String.raw`@[A-Za-z-]+|!important`),
      // A colour literal before the selector rule, so #fff is a colour and
      // #main is an id.
      codeRule("number", String.raw`#[0-9a-fA-F]{3,8}\b`),
      codeRule("tag", String.raw`[.#][A-Za-z_-][A-Za-z0-9_-]*`),
      codeRule("attr", String.raw`--[A-Za-z0-9_-]+|[A-Za-z-]+(?=\s*:)`),
      codeRule(
        "number",
        String.raw`[+-]?(?:\d*\.)?\d+(?:px|em|rem|ex|ch|vh|vw|vmin|vmax|%|s|ms|deg|turn|fr|pt)?`
      ),
      codeRule("punct", String.raw`[{}();:,>+~*]+`),
    ],
    keywords: new Set(),
    builtins: new Set(),
  },
  markdown: {
    // No word rule: prose must stay prose. Only the marks are coloured.
    rules: [
      codeRule("comment", String.raw`^ {0,3}>[^\n]*`, "m"),
      codeRule("keyword", String.raw`^ {0,3}#{1,6}[^\n]*`, "m"),
      codeRule("punct", String.raw`^ {0,3}(?:[-*+]|\d{1,9}[.)])(?= )`, "m"),
      codeRule("string", String.raw`\x60{1,3}[^\x60\n]+\x60{1,3}`),
      codeRule("function", String.raw`!?\[[^\]\n]*\]\([^)\n]*\)`),
      codeRule("tag", String.raw`\*\*[^*\n]+\*\*|__[^_\n]+__`),
      codeRule("tag", String.raw`\*[^*\n]+\*|_[^_\n]+_`),
    ],
    keywords: new Set(),
    builtins: new Set(),
  },
};

function codeGrammarFor(language) {
  if (typeof language !== "string" || language === "") return null;
  const key = CODE_LANGUAGE_ALIASES[language.toLowerCase()];
  return key ? CODE_GRAMMARS[key] : null;
}

// True when the identifier at `after` is being called. The cheapest signal that
// separates a function name from a variable, and the only structure this
// scanner attempts.
function isCodeCallSite(code, after) {
  let index = after;
  while (index < code.length && (code[index] === " " || code[index] === "\t")) index += 1;
  return code[index] === "(";
}

// Returns runs of { type, text } covering `code` exactly — concatenating the
// texts reproduces the input character for character, which is what lets the
// painted block still be selected and copied as the code it is. Returns null
// when the language is unknown or the block blew a bound, which the caller
// reads as "leave it plain".
export function tokenizeCode(code, language) {
  const grammar = codeGrammarFor(language);
  if (!grammar) return null;

  const tokens = [];
  let plain = "";
  const flushPlain = () => {
    if (plain !== "") {
      tokens.push({ type: "", text: plain });
      plain = "";
    }
  };

  let index = 0;
  while (index < code.length) {
    let match = null;
    for (const rule of grammar.rules) {
      rule.re.lastIndex = index;
      const found = rule.re.exec(code);
      if (found && found[0].length > 0) {
        match = { type: rule.type, text: found[0] };
        break;
      }
    }
    if (!match) {
      // Nothing claims this character — whitespace, an operator no grammar
      // lists, a CJK identifier. It joins the running plain run.
      plain += code[index];
      index += 1;
      continue;
    }
    if (match.type === "word") {
      const word = grammar.foldWords ? match.text.toUpperCase() : match.text;
      if (grammar.keywords.has(word)) match.type = "keyword";
      else if (grammar.builtins.has(word)) match.type = "builtin";
      else if (isCodeCallSite(code, index + match.text.length)) match.type = "function";
      else match.type = "";
    }
    if (match.type === "") {
      plain += match.text;
    } else {
      flushPlain();
      tokens.push(match);
    }
    index += match.text.length;
    if (tokens.length > CODE_HIGHLIGHT_MAX_TOKENS) return null;
  }
  flushPlain();
  return tokens;
}

// Text nodes for the unclassified runs, one span per classified run. No
// innerHTML, no string concatenation into markup — the same structural
// argument the Markdown renderer above rests on.
function paintCodeTokens(node, tokens) {
  node.textContent = "";
  for (const token of tokens) {
    if (token.type === "") {
      node.appendChild(document.createTextNode(token.text));
      continue;
    }
    const span = document.createElement("span");
    span.className = `tok-${token.type}`;
    span.textContent = token.text;
    node.appendChild(span);
  }
}

function scheduleCodeHighlight(node, code, language) {
  if (!codeGrammarFor(language)) return;
  if (code.length > CODE_HIGHLIGHT_MAX_CHARS) return;
  // Counted rather than split: a 100 KB block should not be copied into an
  // array just to be rejected.
  let lines = 1;
  for (let index = 0; index < code.length; index += 1) {
    if (code[index] === "\n") lines += 1;
    if (lines > CODE_HIGHLIGHT_MAX_LINES) return;
  }
  scheduleIdleWork(() => {
    const tokens = tokenizeCode(code, language);
    // A block committed mid-stream is already on screen; this replaces its one
    // text node with the coloured runs. The turn it belongs to may since have
    // been superseded, in which case the node is detached and this paints
    // something nobody sees — cheap, and cheaper than tracking ownership.
    if (tokens) paintCodeTokens(node, tokens);
  });
}

// Inline scanning. Written as a scanner rather than a chain of replacements
// because replacements operate on a string, and a string is exactly the thing
// that must never come back as markup.
function parseInlineInto(parent, text) {
  let buffer = "";
  const flush = () => {
    if (buffer !== "") {
      parent.appendChild(document.createTextNode(buffer));
      buffer = "";
    }
  };

  let index = 0;
  while (index < text.length) {
    const char = text[index];

    if (char === "\\" && index + 1 < text.length && INLINE_ESCAPABLE.includes(text[index + 1])) {
      buffer += text[index + 1];
      index += 2;
      continue;
    }
    if (char === "\n") {
      flush();
      parent.appendChild(document.createElement("br"));
      index += 1;
      continue;
    }
    if (char === "`") {
      const span = matchCodeSpan(text, index);
      if (span) {
        flush();
        const node = document.createElement("code");
        node.className = "md-code-inline";
        node.textContent = span.content;
        parent.appendChild(node);
        index = span.end;
        continue;
      }
    }
    if (char === "[") {
      const link = matchLink(text, index);
      if (link) {
        flush();
        parent.appendChild(buildLink(link));
        index = link.end;
        continue;
      }
    }
    if (char === "*" || char === "_" || char === "~") {
      const emphasis = matchEmphasis(text, index);
      if (emphasis) {
        flush();
        const node = document.createElement(emphasis.tag);
        parseInlineInto(node, emphasis.content);
        parent.appendChild(node);
        index = emphasis.end;
        continue;
      }
    }
    buffer += char;
    index += 1;
  }
  flush();
}

function matchCodeSpan(text, start) {
  let run = 0;
  while (text[start + run] === "`") run += 1;
  const fence = "`".repeat(run);
  const close = text.indexOf(fence, start + run);
  if (close < 0) return null;
  return { content: text.slice(start + run, close), end: close + run };
}

function matchEmphasis(text, start) {
  const char = text[start];
  const double = text.slice(start, start + 2);
  const candidates = char === "~"
    ? [{ marker: "~~", tag: "del" }]
    : [
        { marker: double === char + char ? double : null, tag: "strong" },
        { marker: char, tag: "em" },
      ];
  for (const candidate of candidates) {
    if (!candidate.marker) continue;
    if (text.slice(start, start + candidate.marker.length) !== candidate.marker) continue;
    const from = start + candidate.marker.length;
    const close = text.indexOf(candidate.marker, from);
    // Empty emphasis is not emphasis; leaving it as literal text is what a
    // reader would expect from "**" typed on its own.
    if (close < 0 || close === from) continue;
    return { tag: candidate.tag, content: text.slice(from, close), end: close + candidate.marker.length };
  }
  return null;
}

function matchLink(text, start) {
  const labelEnd = text.indexOf("]", start + 1);
  if (labelEnd < 0 || text[labelEnd + 1] !== "(") return null;
  const urlEnd = text.indexOf(")", labelEnd + 2);
  if (urlEnd < 0) return null;
  const label = text.slice(start + 1, labelEnd);
  const href = text.slice(labelEnd + 2, urlEnd).trim();
  if (label === "" || href === "" || /\s/u.test(href)) return null;
  return { label, href, end: urlEnd + 1 };
}

// Only http and https become links. Anything else — javascript:, data:, a
// custom scheme another installed app registered — is rendered as the literal
// text the model wrote, so the user can see exactly what was proposed without
// the renderer offering to follow it.
function buildLink(link) {
  const safe = /^https?:\/\//iu.test(link.href);
  if (!safe) {
    const fallback = document.createElement("span");
    fallback.className = "md-link-refused";
    fallback.textContent = `[${link.label}](${link.href})`;
    return fallback;
  }
  const anchor = document.createElement("a");
  anchor.className = "md-link";
  anchor.setAttribute("href", link.href);
  // rel is belt-and-braces: the shim intercepts the click and hands the URL to
  // Go before any navigation happens, so the window is never handed over.
  anchor.setAttribute("rel", "noopener noreferrer");
  parseInlineInto(anchor, link.label);
  return anchor;
}

// Clipboard access, or null when there is none.
//
// The UI origin is http://127.0.0.1, which browsers treat as a secure context,
// so this is present in the shipped shell. It is still checked because the
// behaviour suite runs the same code in a VM with no navigator at all, and
// because a missing clipboard should remove the affordance rather than break
// the message.
const COPY_FEEDBACK_MS = 1200;

function clipboardWriter() {
  const clipboard = globalThis.navigator?.clipboard;
  return typeof clipboard?.writeText === "function" ? clipboard : null;
}

export function buildCopyButton(text, label) {
  const clipboard = clipboardWriter();
  if (!clipboard || typeof text !== "string" || text === "") return null;
  const button = document.createElement("button");
  button.type = "button";
  button.className = "message-action";
  button.textContent = label;
  button.addEventListener("click", () => {
    // The result is reported on the button itself rather than in the status
    // card: this is a per-message action, and a global status line would make
    // every copy look like an application event.
    Promise.resolve(clipboard.writeText(text)).then(
      () => {
        button.textContent = "Copied";
        setTimeout(() => {
          button.textContent = label;
        }, COPY_FEEDBACK_MS);
      },
      () => {
        button.textContent = "Copy failed";
        setTimeout(() => {
          button.textContent = label;
        }, COPY_FEEDBACK_MS);
      }
    );
  });
  return button;
}
