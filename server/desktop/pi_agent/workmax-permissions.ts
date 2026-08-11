/**
 * WorkMax permission gate — written by the WorkMax sidecar before every
 * approval-mode turn; do not edit (edits are overwritten).
 *
 * Bridges pi's tool loop into WorkMax's unified approval UI: every write/edit
 * tool call raises a ui.select whose TITLE is a machine-readable envelope —
 *
 *   workmax-approval:<toolName>:<target basename>
 *
 * — and whose options are the four unified decisions. The Go sidecar
 * intercepts the resulting extension_ui_request frame (it never reaches a
 * human as a dialog), runs the shared approval flow (auto-allow / session
 * grants / renderer card / timeout), and answers with extension_ui_response
 * {value: "<decision>"}. Any allow_* value lets the tool run; "deny" or a
 * cancelled/undefined answer blocks it.
 *
 * A small path guard mirrors the Go-side pathguard for the write surface:
 * traversal, absolute paths outside the workspace, and sensitive names are
 * blocked outright, asking nobody — but ANNOUNCED first, through a second
 * envelope —
 *
 *   workmax-blocked:<toolName>:<target basename>:<reason code>
 *
 * — so the sidecar can narrate the refusal as the tool_denied frame the other
 * engine emits for its own guard. Its answer is discarded; only the delivery
 * matters. Read-only tools (read/grep/find/ls) pass untouched.
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const APPROVAL_PREFIX = "workmax-approval:";
// A guard block is announced before it is returned, through the same ui.select
// channel: the Go sidecar is the only thing that can narrate it to the user,
// and without this the refusal reached the screen as a bare "failed" with the
// reason — traversal, a sensitive name, an escape from the workspace — lost
// inside the subprocess. The answer is discarded; the round trip exists only
// so the frame is delivered before the tool call ends.
const BLOCKED_PREFIX = "workmax-blocked:";
const DECISIONS = ["allow_once", "allow_session", "allow_always", "deny"];
const GATED_TOOLS = new Set(["write", "edit"]);
const SENSITIVE_NAMES = [".env", ".ssh", ".aws", "id_rsa", "credentials", "authorized_keys"];

/**
 * The Go guard's filepath.Clean, as much of it as this comparison needs:
 * backslashes to slashes, runs of separators collapsed, "." segments dropped,
 * trailing separator removed. Without it "/ws//notes.md" and "/ws/./notes.md"
 * are strings that do not start with "/ws/" — and a write into the thread's
 * own workspace was refused as an escape. ".." is left alone on purpose; it is
 * rejected outright a few lines below, never resolved.
 */
function normalize(p: string): string {
	const slashed = p.replace(/\\/g, "/");
	const segments = slashed.split("/");
	const out: string[] = [];
	for (let i = 0; i < segments.length; i += 1) {
		const seg = segments[i];
		if (seg === "" && i !== 0) continue; // "//" and a trailing "/"
		if (seg === ".") continue;
		out.push(seg);
	}
	const joined = out.join("/");
	// A path that cleaned down to nothing was "/" or "." — keep it truthy so
	// the caller's emptiness checks still mean what they say.
	return joined === "" ? (slashed.startsWith("/") ? "/" : ".") : joined;
}

function basename(p: string): string {
	const parts = normalize(p).split("/").filter((s) => s !== "");
	return parts.length > 0 ? parts[parts.length - 1] : p;
}

function isAbsolute(p: string): boolean {
	return p.startsWith("/") || /^[a-zA-Z]:[\\/]/.test(p);
}

/**
 * Mirrors the Go pathguard's order: traversal first, workspace fast-allow
 * second (files inside the workspace are the user's to have, whatever their
 * name), sensitive-name denylist third, and any absolute path that survived
 * is outside the workspace.
 *
 * Returns a machine-readable REASON CODE to block, or null to proceed. A code
 * rather than a sentence because the sentence the user reads is written on the
 * Go side, in the user's language, next to the other two denial reasons.
 */
function pathBlockReason(rawPath: string, roots: string[]): string | null {
	const p = normalize(rawPath);
	for (const seg of p.split("/")) {
		if (seg === "..") return "traversal";
	}
	if (isAbsolute(p)) {
		for (const raw of roots) {
			if (raw === "") continue;
			const root = normalize(raw);
			if (p === root || p.startsWith(root === "/" ? "/" : root + "/")) return null;
		}
	}
	const lower = p.toLowerCase();
	for (const name of SENSITIVE_NAMES) {
		if (lower.includes(name)) return "sensitive";
	}
	if (isAbsolute(p)) return "outside";
	return null;
}

/**
 * The workspace, spelled every way it can legitimately appear.
 *
 * WORKMAX_PI_WORKSPACE is the sidecar's own string — the authority, because it
 * is the same value the rest of the sidecar reasons about. process.cwd() is
 * kept beside it because Node resolves symlinks and the sidecar does not: on
 * macOS a data dir under /tmp or $TMPDIR reaches the subprocess as
 * /private/... , and a guard that knew only one of the two spellings refused
 * every write into the thread's OWN workspace. Measured — it made approval
 * mode useless wherever any component of the data dir was a link.
 */
function workspaceRoots(): string[] {
	const env = typeof process !== "undefined" && process.env ? process.env : undefined;
	const declared = env && typeof env.WORKMAX_PI_WORKSPACE === "string" ? env.WORKMAX_PI_WORKSPACE : "";
	const cwd = typeof process !== "undefined" && process.cwd ? process.cwd() : "";
	return [declared, cwd];
}

export default function (pi: ExtensionAPI) {
	pi.on("tool_call", async (event, ctx) => {
		const tool = event.toolName;
		if (!GATED_TOOLS.has(tool)) return undefined; // read surface: pass, no card

		const input = (event.input ?? {}) as Record<string, unknown>;
		let rawPath = "";
		for (const key of ["path", "file_path", "filepath"]) {
			const v = input[key];
			if (typeof v === "string" && v !== "") {
				rawPath = v;
				break;
			}
		}
		const target = rawPath === "" ? "" : basename(rawPath).slice(0, 80);
		if (rawPath !== "") {
			const why = pathBlockReason(rawPath, workspaceRoots());
			if (why) {
				await ctx.ui.select(BLOCKED_PREFIX + tool + ":" + target + ":" + why, ["ack"]);
				return { block: true, reason: "WorkMax path guard: " + why };
			}
		}

		const choice = await ctx.ui.select(APPROVAL_PREFIX + tool + ":" + target, DECISIONS);
		if (typeof choice !== "string" || !choice.startsWith("allow")) {
			// "deny", a cancelled dialog, or a timeout all block the same way.
			return { block: true, reason: "the user declined this tool call" };
		}
		return undefined;
	});
}
