// The renderer's language, and the copy that follows it.
//
// English is the key. There is no message-id layer: the shipped markup and
// the string literals in the modules ARE the English catalog, and this file
// carries the other languages as a mapping from that English. That is the
// gettext bet, and it is the right one here for two reasons. First, the
// catalog can never drift from the interface — an untranslated string shows
// as English, not as a bare identifier. Second, the four hundred call sites
// do not each need a name invented for them, which is the difference between
// this being one file and being a two-week renaming project.
//
// The locale comes from <html lang>, which the shell resolved BEFORE the
// first frame (uiserver.go asks the sidecar, which asks the stored
// preference and then the machine). The renderer never guesses: by the time
// any of this runs, the document already says what language it is in.
//
// Switching languages reloads the window (see setLanguage in renderer.js).
// The theme switch went to great lengths to avoid a reload because a flash
// of wrong colour is jarring; language is different — every string on screen
// changes at once, a reload takes under a second on a local page, and a
// live re-translation pass would need every dynamic string's inputs kept
// around for replay. One honest reload beats that machinery.

// currentLocale reads the document's language. "en" for anything unexpected:
// the markup ships in English, so English is what an unresolved page is
// already showing.
export function currentLocale() {
  const lang = document.documentElement?.lang || "en";
  return lang === "zh" ? "zh" : "en";
}

// t translates one string, with optional {name} interpolation.
//
// English input, translated output — or the input itself when the locale is
// English or the catalog has no entry. Interpolation happens AFTER lookup, so
// the catalog's word order wins: "+{n} more" may become "另 {n} 项" with the
// number wherever Chinese wants it.
export function t(text, params) {
  let out = text;
  if (currentLocale() === "zh") {
    const hit = ZH.get(text);
    if (hit !== undefined) out = hit;
  }
  if (params) {
    out = out.replace(/\{(\w+)\}/g, (match, name) =>
      Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : match
    );
  }
  return out;
}

// translatePage rewrites the static markup in place, once, at boot.
//
// It walks rather than queries — the DOM the behaviour suite runs under is a
// deliberately small stub — and it touches exactly four things: an element's
// single text child, and the placeholder / title / aria-label attributes.
// Those are the only places the shipped markup keeps copy.
//
// Safe to run exactly because it runs at boot: the document holds only the
// static skeleton then, so a dictionary hit IS static chrome. Running this
// against a populated transcript would be a different, wrong, program.
export function translatePage() {
  if (currentLocale() !== "zh") return;
  const visit = (node) => {
    if (!node) return;
    if (typeof node.getAttribute === "function" && typeof node.setAttribute === "function") {
      for (const attr of ["placeholder", "title", "aria-label"]) {
        const value = node.getAttribute(attr);
        if (value) {
          const hit = ZH.get(value.trim());
          if (hit !== undefined) node.setAttribute(attr, hit);
        }
      }
    }
    // childNodes, not children: an element like the New chat row is MIXED
    // content — an icon span and then the words as a bare text node — and
    // children (elements only) walks straight past the words. Each text node
    // is looked up on its own; an element that holds only text falls through
    // to the same path via its single text child.
    const kids = Array.from(node.childNodes || node.children || []);
    if (kids.length === 0) {
      const text = node.textContent;
      if (typeof text === "string" && text.trim()) {
        const hit = ZH.get(text.trim().replace(/\s+/g, " "));
        if (hit !== undefined) node.textContent = hit;
      }
      return;
    }
    for (const child of kids) {
      if (child.nodeType === 3) {
        const text = child.textContent;
        if (typeof text === "string" && text.trim()) {
          const hit = ZH.get(text.trim().replace(/\s+/g, " "));
          if (hit !== undefined) child.textContent = hit;
        }
        continue;
      }
      visit(child);
    }
  };
  visit(document.body);
  if (document.title && ZH.has(document.title)) {
    document.title = ZH.get(document.title);
  }
}

// hasTranslation answers whether a string has a Chinese entry. It exists for
// the behaviour suite's coverage gate: every translatable string the shipped
// markup carries must have an entry, and the gate needs a way to ask without
// this module exporting its catalog for mutation.
export function hasTranslation(text) {
  return ZH.has(text);
}

// The Chinese catalog. Keys are the exact English strings, whitespace
// collapsed. A missing entry falls back to English — add here when copy
// lands, and keep the voice of server/desktop/i18n/messages/zh.json: say
// what happened, then what the reader can do; no exclamation marks.
//
// Deliberately untranslated: base-URL and model-id EXAMPLES (they are
// literal values the user might type), and the product name "WorkMax".
const ZH = new Map(Object.entries({
  // --- Rail -------------------------------------------------------------
  "New chat": "新对话",
  "Projects": "项目",
  "Schedule": "计划任务",
  "Plugins": "插件",
  "Sidebar sections": "侧栏分区",
  "Cached threads": "本地缓存的对话",
  "Local": "本地",
  "On this machine": "在这台机器上",
  "Settings…": "设置…",
  "Appearance…": "外观…",
  "About WorkMax": "关于 WorkMax",
  "Settings": "设置",
  "Hide sidebar": "收起侧栏",
  "Hide sidebar (⌘\\)": "收起侧栏（⌘\\）",
  "Previous conversation": "上一个对话",
  "Next conversation": "下一个对话",
  "Search (⌘K)": "搜索（⌘K）",
  "Search conversations and actions": "搜索对话与操作",
  "No scheduled tasks yet.": "还没有计划任务。",
  "Automations you schedule will run here, unattended, and queue their results for review.":
    "你安排的自动化任务会在这里无人值守地运行，结果排队等你审阅。",
  "No plugins installed.": "还没有安装插件。",
  "Skills and connectors extend what the agent can reach. They will be listed here.":
    "技能与连接器扩展 agent 能触达的范围，安装后会列在这里。",

  // --- Home / composer --------------------------------------------------
  "What should we make today?": "今天想做点什么？",
  "Pick a conversation, or start a new one.": "选择一个对话，或者开始新的。",
  "Ask anything…": "想问什么都可以…",
  "Message WorkMax Agent": "发给 WorkMax Agent 的消息",
  "Attach files": "添加附件",
  "Send": "发送",
  "Send (⌘↵)": "发送（⌘↵）",
  "Stop": "停止",
  "Ask": "询问",
  "Agent skill": "Agent 技能",
  "Skill": "技能",
  "New conversation": "新对话",
  "Connect a WorkMax account": "连接 WorkMax 账号",
  "Cloud models, and your conversations follow you to other devices.":
    "使用云端模型，对话跟随你到其他设备。",
  "Use a local model": "使用本地模型",
  "Point WorkMax at an endpoint on this machine. Nothing leaves it.":
    "把 WorkMax 指向这台机器上的 endpoint，数据不出本机。",

  // --- Thread panel -----------------------------------------------------
  "Rename": "重命名",
  "Export": "导出",
  "Conversation name": "对话名称",
  "Save": "保存",
  "Cancel": "取消",
  "Delete": "删除",
  "Resume": "继续",
  "Retry": "重试",
  "Continue": "继续",
  "Dismiss": "忽略",
  "Dismiss this message": "忽略这条消息",
  "Interrupted": "已中断",
  "Interrupted response": "被中断的回答",
  "Retry this interrupted response using the same request.":
    "用同样的请求重试这条被中断的回答。",
  "↓ Jump to latest": "↓ 跳到最新",
  "Loading...": "加载中…",
  "New identity": "新身份",
  "Name": "名称",
  "Title": "标题",
  "Create": "创建",
  "Add": "添加",

  // --- Workspace panel --------------------------------------------------
  "Changes": "改动",
  "Produced": "产出",
  "Attached": "附件",
  "Retrieved": "召回",
  "Open folder": "打开文件夹",
  "Files you attach and files the agent writes appear here.":
    "你添加的附件和 agent 写出的文件会出现在这里。",
  "Hide workspace panel": "收起工作区面板",
  "Show mind panel": "打开 mind 面板",

  // --- Mind panel -------------------------------------------------------
  "Mind": "Mind",
  "Minds": "Minds",
  "New mind": "新建 mind",
  "New mind name": "新 mind 的名字",
  "A mind: a brain that reasons, a cerebellum of practised skills, and a memory of what it has been taught.":
    "一个 mind：负责推理的大脑、熟练技能的小脑，以及教给它的记忆。",
  "Anatomy": "结构",
  "Brain": "大脑",
  "Cerebellum": "小脑",
  "Memory": "记忆",
  "Material": "材料",
  "Teach": "教它",
  "Teach it something": "教它点什么",
  "Paste what this mind should remember.": "粘贴这个 mind 应该记住的内容。",
  "What it is for": "它是做什么的",
  "What it knows": "它知道什么",
  "A short note about this mind's purpose.": "一句话说明这个 mind 的用途。",
  "Answer only in bullet points.": "只用要点作答。",
  "Use the identity's model": "使用当前身份的模型",
  "What this identity thinks with, and what it has been taught.":
    "这个身份用什么思考，以及教过它什么。",
  "Compensation bands 2026": "2026 薪酬带宽",

  // --- Settings: shell --------------------------------------------------
  "Close settings": "关闭设置",
  "Model": "模型",
  "Account": "账号",
  "Appearance": "外观",
  "About": "关于",

  // --- Settings: model --------------------------------------------------
  "Where a turn runs, and which model answers it.": "每一轮在哪里运行、由哪个模型回答。",
  "Route": "路由",
  "Official runs on your workmax.app quota. Local runs on an endpoint you host.":
    "官方路由使用你的 workmax.app 配额；本地路由使用你自己托管的 endpoint。",
  "Official (workmax.app quota)": "官方（workmax.app 配额）",
  "Local / self-hosted": "本地 / 自托管",
  "Protocol": "协议",
  "OpenAI-compatible": "OpenAI 兼容",
  "Anthropic-compatible": "Anthropic 兼容",
  "Base URL": "Base URL",
  "Leave empty to run here on your official model.": "留空则在这里使用你的官方模型。",
  "Model ID": "模型 ID",
  "The name your endpoint answers to, spelled its way.":
    "你的 endpoint 认得的模型名，按它的写法拼写。",
  "API key": "API key",
  "Leave blank to keep the stored key": "留空保持已存的 key 不变",
  "Clear the stored API key": "清除已存的 API key",

  // --- Settings: account ------------------------------------------------
  "Who you are on this machine, and what that identity is connected to.":
    "你在这台机器上的身份，以及它连接到哪里。",
  "Identities on this machine": "这台机器上的身份",
  "New account name": "新账号名",
  "WorkMax connection": "WorkMax 连接",
  "Connect account": "连接账号",
  "Connection": "连接",
  "Disconnect": "断开连接",
  "Email": "邮箱",
  "Password": "密码",

  // --- Settings: appearance ---------------------------------------------
  "How this window looks, whatever the desktop does.": "这个窗口的样子，不受桌面影响。",
  "Theme": "主题",
  "System follows the desktop. Light and dark stay put.":
    "系统即跟随桌面；亮色与暗色固定不变。",
  "System": "系统",
  "Light": "亮色",
  "Dark": "暗色",
  "Density": "密度",
  "How much air sits between things. Text stays the same size.":
    "元素之间留多少空隙，文字大小不变。",
  "Compact": "紧凑",
  "Standard": "标准",
  "Comfortable": "宽松",
  "Language": "语言",
  "Which language this window speaks. System follows the machine.":
    "这个窗口说哪种语言；系统即跟随本机设置。",
  "English": "English",
  "中文": "中文",

  // --- Settings: about --------------------------------------------------
  "What is running here, and the terms it ships under.": "这里运行着什么，以及它的发布条款。",
  "This app and the sidecar it launched.": "这个应用，以及它启动的 sidecar。",
  "Version": "版本",
  "License": "许可证",
  "Source code · AGPL-3.0": "源代码 · AGPL-3.0",
  "WorkMax is free software: read it, change it, run your own.":
    "WorkMax 是自由软件：可以阅读、修改、自行运行。",
  "How it works": "工作原理",
  "Starting sidecar bridge...": "正在启动 sidecar 桥…",

  // --- Sidebar threads (threads.js) --------------------------------------
  "now": "刚刚",
  "{n}m": "{n} 分钟",
  "{n}h": "{n} 小时",
  "{n}d": "{n} 天",
  "Untitled thread": "未命名对话",
  "Today": "今天",
  "Previous 7 days": "最近 7 天",
  "Older": "更早",
  "Pinned": "置顶",
  "Pin": "置顶",
  "Unpin": "取消置顶",
  "Pin {name}": "置顶 {name}",
  "Unpin {name}": "取消置顶 {name}",
  "More": "更多",
  "More actions for {name}": "{name} 的更多操作",
  "Move to project": "移到项目",
  "New project…": "新建项目…",
  "Remove from project": "移出项目",
  "Resume syncing": "恢复同步",
  "Keep on this machine": "只留在本机",
  "Project name": "项目名称",
  "Project for {name}": "{name} 的项目",
  "Set project for {name}": "为 {name} 设置项目",
  "Local only": "仅本机",
  "Syncing": "同步中",
  "Resume syncing {name}": "恢复同步 {name}",
  "Stop syncing {name}": "停止同步 {name}",
  "Confirm": "确认",
  "Delete {name}": "删除 {name}",
  "{n} messages": "{n} 条消息",
  "No cached threads yet.": "还没有缓存的对话。",
  "Could not update the pin": "置顶状态没有改成",
  "Could not set the project": "项目没有设置成",
  "Could not change the sync setting": "同步设置没有改成",
  "That conversation has never left this machine.": "这个对话从未离开过这台机器。",
  "That conversation still has a response in flight. Stop it first.":
    "这个对话还有回答在进行中，先停止它。",
  "Could not delete the conversation.": "对话没有删除成。",
  "Conversation deleted from this machine.": "对话已从这台机器删除。",
  "Kept on this machine. Existing cloud copies are untouched.":
    "已保留在这台机器上；云端已有的副本不受影响。",
  "Syncing to WorkMax again.": "已恢复同步到 WorkMax。",

  // --- Quick switcher (threads.js) ----------------------------------------
  "Start a conversation": "开始一个对话",
  "Pin this conversation": "置顶这个对话",
  "Unpin this conversation": "取消置顶这个对话",
  "Export as Markdown": "导出为 Markdown",
  "Open model settings": "打开模型设置",
  "Local route, protocol, API key": "本地路由、协议、API key",
  "Appearance: {label}": "外观：{label}",
  "Current": "当前",
  "Actions": "操作",
  "In messages": "消息内容",
  "Nothing matches.": "没有匹配的结果。",
  "You: ": "你：",

  // --- Composer / create / recovery (composer.js) -------------------------
  "File upload unavailable": "文件上传不可用",
  "Remove": "移除",
  "Prompt: {preview}": "提示词：{preview}",
  "Resuming...": "恢复中…",
  "Checking...": "检查中…",
  "Dismissing...": "忽略中…",
  "Retrying the interrupted request safely.": "正在安全地重试被中断的请求。",
  "Your signed-in account changed. Select a thread again.":
    "登录账号变了，请重新选择一个对话。",
  "This conversation is yours and stays here. To send a prompt, connect a WorkMax account or set a local model under Models.":
    "这个对话属于你，留在本机。要发送提示词，请连接 WorkMax 账号，或在「模型」里设置一个本地模型。",
  "Browse and organize freely. To send a prompt, connect a WorkMax account or set a local model under Models.":
    "浏览与整理不受限制。要发送提示词，请连接 WorkMax 账号，或在「模型」里设置一个本地模型。",
  "Starting a conversation keeps it on this machine.": "开始的对话会留在这台机器上。",
  "Agent streaming is unavailable in this Desktop build.":
    "这个桌面构建不支持 Agent 流式响应。",
  "Loading available skills...": "正在加载可用技能…",
  "Checking interrupted responses...": "正在检查被中断的回答…",
  "No Desktop skill is currently available.": "当前没有可用的桌面技能。",
  "Resuming the interrupted response...": "正在恢复被中断的回答…",
  "Resume or dismiss the interrupted response before sending another prompt.":
    "先继续或忽略被中断的回答，再发送新的提示词。",
  "Confirming persistent cancellation...": "正在确认持久取消…",
  "Stopping this turn...": "正在停止这一轮…",
  "WorkMax Agent is responding...": "WorkMax Agent 正在回答…",
  "Chat only — this local route has no tools.": "仅对话——这个本地路由没有工具。",
  "Live skill details are offline.": "技能详情暂时离线。",

  // --- Starter prompts (composer.js; the prompt half is model input) -------
  "Review what I have": "看看我手上的东西",
  "Review the files I attach. Tell me what is wrong, what is missing, and what to do first.":
    "看看我附上的文件，告诉我哪里有问题、缺什么、先做什么。",
  "Plan the next step": "规划下一步",
  "Help me plan the next step. Ask me the two questions that would change the plan.":
    "帮我规划下一步，并问我两个最能改变这个计划的问题。",
  "Explain this simply": "用大白话讲讲",
  "Explain what I have here in plain language, then list the risks I should not ignore.":
    "用大白话讲讲我手上的这些东西，再列出我不该忽视的风险。",

  // --- Home empty state (composer.js) -------------------------------------
  "You're working as {name}": "你正以 {name} 的身份工作",
  "You're working on this machine": "你正在这台机器上工作",
  "Your conversations and files are already here and already yours. One thing left: where should the model run?":
    "对话和文件已经在这里，也已经属于你。只剩一件事：模型在哪里运行？",
  "What should we make, {name}?": "{name}，今天想做点什么？",
  "This Desktop build can continue existing threads after they appear in local history.":
    "这个桌面构建可以在对话出现在本地历史后继续它们。",

  // --- Thread create (composer.js) -----------------------------------------
  "Thread creation is unavailable in this Desktop build": "这个桌面构建不支持创建对话",
  "Creating...": "创建中…",
  "Retry sync": "重试同步",
  "Cannot retry": "无法重试",
  "Enter a valid name and choose an available skill.": "输入有效名称，并选择一个可用技能。",
  "Secure thread identity generation is unavailable.": "无法安全地生成对话标识。",
  "Thread creation canceled. A late result will be ignored.":
    "创建已取消；迟到的结果会被忽略。",
  "New chat canceled.": "新对话已取消。",
  "Retry or cancel the current thread draft before switching.":
    "先重试或取消当前的对话草稿，再切换。",
  "Cancel the current thread draft before switching.": "先取消当前的对话草稿，再切换。",
  "Starting a conversation...": "正在开始对话…",
  "The cloud thread is ready. Retry sync with the same identity, or cancel this draft.":
    "云端对话已就绪。用同一标识重试同步，或取消这份草稿。",
  "Thread created. Local history is still syncing.": "对话已创建，本地历史还在同步。",
  "Thread created. Ready for the first prompt.": "对话已创建，可以发第一条提示词了。",
  "Thread recovered. Ready for the first prompt.": "对话已找回，可以发第一条提示词了。",
  "Authentication is required. Cancel this draft, then sign in again.":
    "需要先登录。取消这份草稿，然后重新登录。",
  "Authentication is required before creating a thread.": "创建对话前需要先登录。",
  "This thread identity is already owned elsewhere. Cancel and start a new draft.":
    "这个对话标识已被别处占用。取消后重新起一份草稿。",
  "The generated thread identity cannot be used.": "生成的对话标识不可用。",
  "Local history has an identity conflict. Cancel this draft before refreshing.":
    "本地历史存在身份冲突。先取消这份草稿，再刷新。",
  "Local history could not safely accept this thread.": "本地历史无法安全接收这个对话。",
  "This thread could not be completed. Retry keeps the same identity.":
    "这个对话没有完成。重试会沿用同一标识。",
  "The presentation thread could not be completed.": "对话没有完成。",
  "Thread creation was rejected. Cancel this draft before continuing.":
    "创建被拒绝。先取消这份草稿，再继续。",
  "The presentation thread cannot be retried.": "这个对话无法重试。",
  "This existing thread is paused and cannot be continued here. Cancel this draft before continuing.":
    "这个已有对话处于暂停状态，无法在这里继续。先取消这份草稿。",
  "The existing conversation is paused.": "已有的对话处于暂停状态。",
  "This thread could not be completed. Retry keeps the same identity, or cancel this draft.":
    "这个对话没有完成。重试会沿用同一标识，或者取消这份草稿。",
  "The conversation could not be started.": "对话没能开始。",

  // --- Settings persistence (renderer.js) ----------------------------------
  "Appearance changed for now, but it could not be saved.":
    "外观已切换，但没能保存下来。",
  "Density changed for now, but it could not be saved.": "密度已切换，但没能保存下来。",
  "This build cannot store a language preference.": "这个构建无法存储语言偏好。",
  "The language could not be saved.": "语言没能保存下来。",

  // --- Model settings (renderer.js) ----------------------------------------
  "Works with Ollama, LM Studio, vLLM and similar. Enables the agent tool loop when the pi runtime is available.":
    "适用于 Ollama、LM Studio、vLLM 等。pi 运行时可用时启用 agent 工具循环。",
  "Works with servers speaking the Anthropic Messages API. Enables the agent tool loop when a Claude CLI is available.":
    "适用于实现 Anthropic Messages API 的服务。Claude CLI 可用时启用 agent 工具循环。",
  "Account default": "账号默认",
  "{name} (default)": "{name}（默认）",
  "{name} — needs {tier}": "{name} — 需要 {tier}",
  "{name} — unavailable": "{name} — 不可用",
  "{name} — no longer offered": "{name} — 已不再提供",
  "Model choice needs a newer desktop shell.": "选择模型需要更新版的桌面外壳。",
  "Connect an account to choose a model.": "连接账号后才能选择模型。",
  "Could not reach WorkMax; the model list may be out of date.":
    "连不上 WorkMax，模型列表可能已过期。",
  "Your plan no longer includes the model you picked. Choose another one to continue on the official route.":
    "你的套餐已不含所选模型。换一个模型才能继续走官方路由。",
  "The model you picked is no longer offered. Choose another one.":
    "所选模型已不再提供，换一个吧。",
  "Plan: {tier}.": "套餐：{tier}。",
  "Model settings need a newer Desktop shell (desktopBridge.settings, alpha.7+).":
    "模型设置需要更新版的桌面外壳（desktopBridge.settings，alpha.7+）。",
  "Model settings bridge unavailable": "模型设置桥不可用",
  "That model is not available on your plan. Pick one that is.":
    "你的套餐没有这个模型。挑一个有的。",
  "API key: stored in Keychain": "API key：已存入钥匙串",
  "API key: not stored": "API key：未存储",
  "Could not save model settings": "模型设置没保存成",
  "Saved. Local model route — you can work without signing in.":
    "已保存。本地模型路由——不登录也能工作。",
  "Saved local model route.": "已保存本地模型路由。",
  "Saved official model route preference.": "已保存官方模型路由偏好。",

  // --- Identity & account (renderer.js) -------------------------------------
  "Connected to WorkMax": "已连接 WorkMax",
  "Sign-in expired": "登录已过期",
  "Local model · this machine": "本地模型 · 这台机器",
  "This machine only": "仅这台机器",
  "While an account is connected, new conversations belong to it. Disconnect to work as this machine's own identities again.":
    "连接账号期间，新对话都归它。断开连接才能重新以本机身份工作。",
  "Connected to a WorkMax account{named} — cloud models and sync are available.":
    "已连接 WorkMax 账号{named}——云端模型与同步可用。",
  "The connected WorkMax account{named} needs signing in again. Local work is unaffected.":
    "已连接的 WorkMax 账号{named}需要重新登录。本地工作不受影响。",
  "No WorkMax account connected. Everything here is local to this machine.":
    "未连接 WorkMax 账号。这里的一切都只在这台机器上。",
  "Sign in again": "重新登录",
  "Disconnecting...": "断开中…",
  "{name} · active": "{name} · 当前",
  "Rename {name}": "重命名 {name}",
  "Delete {name} and all its data": "删除 {name} 及其全部数据",
  "Delete all its data?": "删除它的全部数据？",
  "New name for {name}": "{name} 的新名字",
  "An account with that name already exists": "同名账号已存在",
  "Could not rename account": "账号没重命名成",
  'Renamed to "{name}"': "已重命名为「{name}」",
  "That account has a turn running — stop it first": "那个账号还有回答在进行——先停止它",
  "Switch away from an account before deleting it": "先切换到别的账号，再删除它",
  "Could not delete account": "账号没删除成",
  'Deleted "{name}" and its 1 conversation': "已删除「{name}」及其 1 个对话",
  'Deleted "{name}" and its {n} conversations': "已删除「{name}」及其 {n} 个对话",
  'Deleted "{name}"': "已删除「{name}」",
  "Disconnecting is unavailable in this Desktop build": "这个桌面构建不支持断开连接",
  "Could not disconnect the account": "账号没断开成",
  "Account disconnected. You are working as this machine's identity again.":
    "账号已断开。你重新以这台机器的身份工作。",
  "Could not switch account": "账号没切换成",
  'Switched to "{name}"': "已切换到「{name}」",
  "Switched account": "已切换账号",
  "Account name cannot be empty": "账号名不能为空",
  "Account limit reached": "账号数量已达上限",
  "Could not create account": "账号没创建成",
  'Created "{name}" — click it to switch': "已创建「{name}」——点它即可切换",

  // --- Composer chips (renderer.js) -----------------------------------------
  "local, with tools": "本地，带工具",
  "local": "本地",
  "cloud": "云端",
  "Model: {label} ({where}). Click to change.": "模型：{label}（{where}）。点击可更换。",
  "Model: {label} ({where}). Open settings": "模型：{label}（{where}）。打开设置",
  "Auto": "自动",
  "Auto: tool calls are approved without asking. Click to require approval.":
    "自动：工具调用免审直接放行。点击改为需要批准。",
  "Ask: each tool call asks before running. Click to auto-approve.":
    "询问：每次工具调用运行前都会询问。点击改为自动放行。",
  "Mind: {name}. Open the mind panel": "Mind：{name}。打开 mind 面板",

  // --- Export & rename (renderer.js) ----------------------------------------
  "Nothing to export yet — this conversation has no messages":
    "还没有可导出的内容——这个对话没有消息",
  "Could not export the conversation": "对话没导出成",
  "Exported 1 message as Markdown — opening the folder":
    "已把 1 条消息导出为 Markdown——正在打开文件夹",
  "Exported {n} messages as Markdown — opening the folder":
    "已把 {n} 条消息导出为 Markdown——正在打开文件夹",
  "Enter a name up to 200 characters.": "输入一个不超过 200 字符的名称。",
  "Could not rename the conversation.": "对话没重命名成。",
  "Conversation renamed.": "对话已重命名。",
  "Unavailable": "不可用",

  // --- Turn lifecycle (renderer.js + transcript.js pill) ---------------------
  "Working": "运行中",
  "Resuming": "恢复中",
  "Stopping": "停止中",
  "Done": "完成",
  "Error": "出错",
  "Stopped": "已停止",
  "Stopped locally": "已在本地停止",
  "Session changed": "会话已变更",
  "The Agent stream returned an invalid event.": "Agent 流返回了无效事件。",
  "The Agent turn could not be started.": "这一轮没能启动。",
  "The recovered Agent stream returned an invalid event.": "恢复的 Agent 流返回了无效事件。",
  "The recovered Agent stream returned an invalid turn identifier.":
    "恢复的 Agent 流返回了无效的轮次标识。",
  "The Agent turn failed.": "这一轮失败了。",
  "Generation stopped.": "生成已停止。",
  "Response completed without text.": "回答结束了，但没有文字。",
  "The recovered response failed ({label}).": "恢复的回答失败了（{label}）。",
  "The recovered response failed.": "恢复的回答失败了。",

  // --- Recovery (renderer.js) ------------------------------------------------
  "Retrying the interrupted request safely...": "正在安全地重试被中断的请求…",
  "Recovery could not connect. Select Resume to try again.":
    "恢复没能连上。点「继续」再试一次。",
  "Response recovery is waiting to retry.": "回答恢复正在等待重试。",
  "The interrupted response could not be resumed yet.": "被中断的回答还没能恢复。",
  "Canceling the interrupted response...": "正在取消被中断的回答…",
  "Interrupted response dismissed.": "被中断的回答已忽略。",
  "Interrupted response was already dismissed.": "被中断的回答此前已被忽略。",
  "Dismiss failed. Try again when the Sidecar is available.":
    "忽略失败。等 sidecar 可用后再试。",
  "The interrupted response could not be dismissed.": "被中断的回答没能忽略掉。",
  "The same request is ready for an explicit recovery attempt.":
    "同一请求已就绪，可以手动发起恢复。",
  "Persistent recovery was not visible yet; the same request is retained locally.":
    "持久恢复记录还看不到；同一请求已保留在本地。",
  "This request is still busy. Select Resume to retry the same request; nothing was started again.":
    "这个请求仍在进行。点「继续」重试同一请求；没有任何新的执行被启动。",
  "This request is still busy. Its local recovery intent was retained; select Resume to retry the same request. Nothing was started again.":
    "这个请求仍在进行。本地已保留恢复意图；点「继续」重试同一请求。没有任何新的执行被启动。",
  "This request is still busy. Checking its persistent recovery state...":
    "这个请求仍在进行。正在检查它的持久恢复状态…",
  "Response interrupted before it produced text.": "回答在产生文字前被中断。",
  "This request is still busy; no new execution was started. Checking recovery state...":
    "这个请求仍在进行；没有启动新的执行。正在检查恢复状态…",
  "Recovery returned invalid data. Select Resume to try the same request again.":
    "恢复返回了无效数据。点「继续」再次重试同一请求。",
  "Local stop completed; persistent dismissal was not confirmed.":
    "本地已停止；持久忽略尚未确认。",
  "Persistent dismissal was not confirmed; the request is retained locally.":
    "持久忽略尚未确认；请求已保留在本地。",
  "The stream stopped locally, but persistent dismissal was not confirmed. Review the interrupted response; it was not replayed.":
    "流已在本地停止，但持久忽略尚未确认。请查看被中断的回答；它没有被重放。",
  "The stream stopped locally, but persistent dismissal was not confirmed. Checking interrupted responses...":
    "流已在本地停止，但持久忽略尚未确认。正在检查被中断的回答…",
  "The Agent turn could not be stopped yet.": "这一轮还没能停下来。",

  // --- Sign-in & boot (renderer.js) ------------------------------------------
  "The connected account needs signing in again. Your local work is still here.":
    "已连接的账号需要重新登录。你的本地工作都还在。",
  "Working as {name} on this machine. Connect an account or a local model to start a conversation.":
    "正以 {name} 的身份在这台机器上工作。连接账号或本地模型即可开始对话。",
  "Working on this machine. Connect an account or a local model to start a conversation.":
    "正在这台机器上工作。连接账号或本地模型即可开始对话。",
  "Another sign-in action is already in progress. Check the current sign-in state.":
    "另一个登录操作正在进行。先确认当前登录状态。",
  "The sign-in request was rejected. Check your details and try again.":
    "登录请求被拒绝。检查填写内容后再试。",
  "The email or password is incorrect. Try again.": "邮箱或密码不对，再试一次。",
  "This sign-in session expired. Start a new sign-in.": "这次登录会话已过期，重新登录。",
  "The sign-in service is temporarily unavailable. Try again shortly.":
    "登录服务暂时不可用，稍后再试。",
  "Sign-in was canceled.": "登录已取消。",
  "App and sidecar are out of sync: the sidecar's answers no longer match this UI. Restart the app; if this persists, reinstall.":
    "应用与 sidecar 不同步：sidecar 的应答已对不上这个界面。重启应用；若仍如此，请重装。",
  "Enter your WorkMax email and password to continue.":
    "输入你的 WorkMax 邮箱和密码以继续。",
  "Completing sign-in securely...": "正在安全地完成登录…",
  "Sign-in complete. Loading your local history...": "登录完成。正在加载本地历史…",
  "Preparing a secure sign-in session...": "正在准备安全登录会话…",
  "Checking your credentials...": "正在校验凭据…",
  "Authenticated{tier}. Reading local cache.": "已登录{tier}。正在读取本地缓存。",
  "This bundled renderer must run inside WorkMax Desktop.":
    "这个打包的界面必须在 WorkMax Desktop 里运行。",
  "Retry or cancel the current thread draft before refreshing.":
    "先重试或取消当前的对话草稿，再刷新。",
  "Cancel the current thread draft before refreshing.": "先取消当前的对话草稿，再刷新。",
  "Checking auth status...": "正在检查登录状态…",

  // --- Layout toggles & misc (renderer.js) ------------------------------------
  "{n}% of the message limit used": "已使用消息长度上限的 {n}%",
  "Could not open the workspace folder.": "工作区文件夹没打开成。",
  "Show sidebar": "展开侧栏",
  "{label} (⌘\\)": "{label}（⌘\\）",
  "Hide {name}": "收起{name}",
  "Show {name}": "打开{name}",
  "workspace panel": "工作区面板",
  "mind panel": "mind 面板",
  "Follow the desktop appearance": "跟随桌面外观",
  "Always light, whatever the system does": "始终亮色，不随系统",
  "Always dark, whatever the system does": "始终暗色，不随系统",
  "match system": "跟随系统",
  "light": "亮色",
  "dark": "暗色",

  // --- Transcript (transcript.js, markdown.js) -----------------------------
  "Show 1 earlier exchange": "显示前 1 轮对话",
  "Show {n} earlier exchanges": "显示前 {n} 轮对话",
  "Response interrupted before text was cached.": "回答在文字缓存前被中断。",
  "No cached messages for this thread yet.": "这个对话还没有缓存的消息。",
  "Loading cached messages...": "正在加载缓存的消息…",
  "You": "你",
  "Copy": "复制",
  "Copy answer": "复制回答",
  "Copy code": "复制代码",
  "Copied": "已复制",
  "Copy failed": "复制失败",
  "Edit and resend": "编辑后重发",
  "Regenerate": "重新生成",

  // --- Work log & approvals (context-panel.js) -----------------------------
  "{n} steps": "{n} 步",
  "{n} blocked": "{n} 步被拦下",
  "+{n} more": "还有 {n} 项",
  "blocked — {reason}": "被拦下——{reason}",
  "blocked": "被拦下",
  "failed": "失败",
  "Awaiting approval": "等待批准",
  "Thinking… {line}": "思考中… {line}",
  "Thinking…": "思考中…",
  "Thought": "思考过程",
  "Allow once": "允许一次",
  "Allowed once": "已允许一次",
  "Allow{named} this session": "本次会话允许{named}",
  "Allowed{named} for this session": "本次会话已允许{named}",
  "Always allow{named}": "始终允许{named}",
  "Always allowed{named}": "已始终允许{named}",
  "Deny": "拒绝",
  "Denied": "已拒绝",
  "Agent requests to run {name} · {target}": "Agent 请求运行 {name} · {target}",
  "Agent requests to run {name}": "Agent 请求运行 {name}",
  "Approvals are unavailable": "审批不可用",
  "Expired": "已过期",
  "The decision could not be delivered. Try again.": "这个决定没送达，再试一次。",
  "Needs your approval": "需要你的批准",
  "Step {n}": "第 {n} 步",
  "Running": "运行中",

  // --- Workspace rail (context-panel.js) -----------------------------------
  "{n} file": "{n} 个文件",
  "{n} files": "{n} 个文件",
  "{n} selected for the next request": "已选 {n} 个用于下一次请求",
  "Missing on disk": "磁盘上已不存在",
  "Uploading…": "上传中…",
  "Open the workspace folder": "打开工作区文件夹",
  "New": "新",

  // --- Mind panel (mind.js) -------------------------------------------------
  "Minds need a newer Desktop shell.": "Minds 需要更新版的桌面外壳。",
  "Minds could not be loaded.": "Minds 没能加载出来。",
  "Could not switch mind.": "mind 没切换成。",
  "Could not create this mind.": "这个 mind 没创建成。",
  "Could not save this mind.": "这个 mind 没保存成。",
  "Could not delete this mind.": "这个 mind 没删除成。",
  "This material could not be taught.": "这份材料没能教进去。",
  "A mind needs a name.": "mind 需要一个名字。",
  "Delete for good": "永久删除",
  "This also erases everything this mind was taught.": "这会同时抹掉教给这个 mind 的一切。",
  "Both a title and some material are needed.": "标题和材料都要填。",
  "Teaching…": "教学中…",
  'Learned "{title}" as {passages}.': "已学会「{title}」，共 {passages}。",
  "Edit": "编辑",
  "Edit {name}": "编辑 {name}",
  "No mind is available on this identity yet.": "这个身份还没有可用的 mind。",
  "Chosen for this mind · {route} route": "为这个 mind 选定 · {route} 路由",
  "From your account · {route} route": "来自你的账号 · {route} 路由",
  "{n} {noun}": "{n} {noun}",
  "{n} passage": "{n} 段",
  "{n} passages": "{n} 段",
  "skill": "个技能",
  "skills": "个技能",
  "passage": "段",
  "passages": "段",
  "material": "份材料",
  "materials": "份材料",
  "Practised through this workspace's skills": "通过这个工作区的技能来练习",
  "No skills are available yet": "还没有可用的技能",
  "Nothing taught yet · recalled on this machine": "还没教过什么 · 在这台机器上召回",
  "Recalled on this machine": "在这台机器上召回",
  "Local recall is unavailable on this build": "这个构建不支持本地召回",

  // --- Search palette ---------------------------------------------------
  "Search conversations, messages and actions": "搜索对话、消息与操作",
  "Search conversations, messages and actions…": "搜索对话、消息与操作…",
}));
