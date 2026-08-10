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
 * blocked outright without asking. Read-only tools (read/grep/find/ls) pass
 * untouched.
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const APPROVAL_PREFIX = "workmax-approval:";
const DECISIONS = ["allow_once", "allow_session", "allow_always", "deny"];
const GATED_TOOLS = new Set(["write", "edit"]);
const SENSITIVE_NAMES = [".env", ".ssh", ".aws", "id_rsa", "credentials", "authorized_keys"];

function normalize(p: string): string {
	return p.replace(/\\/g, "/");
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
 * is outside the workspace. Returns a reason to block, or null to proceed.
 */
function pathBlockReason(rawPath: string, cwd: string): string | null {
	const p = normalize(rawPath);
	for (const seg of p.split("/")) {
		if (seg === "..") return "directory traversal in path";
	}
	if (isAbsolute(p) && cwd !== "") {
		const root = normalize(cwd).replace(/\/+$/, "");
		if (p === root || p.startsWith(root + "/")) return null;
	}
	const lower = p.toLowerCase();
	for (const name of SENSITIVE_NAMES) {
		if (lower.includes(name)) return "path touches a sensitive file (" + name + ")";
	}
	if (isAbsolute(p)) return "absolute path outside the workspace";
	return null;
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
		const cwd = typeof process !== "undefined" && process.cwd ? process.cwd() : "";
		if (rawPath !== "") {
			const why = pathBlockReason(rawPath, cwd);
			if (why) return { block: true, reason: "WorkMax path guard: " + why };
		}

		const target = rawPath === "" ? "" : basename(rawPath).slice(0, 80);
		const choice = await ctx.ui.select(APPROVAL_PREFIX + tool + ":" + target, DECISIONS);
		if (typeof choice !== "string" || !choice.startsWith("allow")) {
			// "deny", a cancelled dialog, or a timeout all block the same way.
			return { block: true, reason: "the user declined this tool call" };
		}
		return undefined;
	});
}
