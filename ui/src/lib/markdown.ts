// Small, safe markdown renderer (escape first, then format) + assistant-message
// rendering with thinking traces, ported from the vanilla app.
import { iconHtml } from "./icons";

export function escapeHtml(s: string): string {
  return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c] as string);
}

function mdInline(escaped: string): string {
  return escaped
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/(^|[^*])\*([^*\n]+)\*/g, "$1<em>$2</em>")
    .replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
}

let cbSeq = 0;
function codeBlockHtml(lang: string, code: string): string {
  const id = "cb" + ++cbSeq;
  return (
    `<div class="code-block"><div class="code-head"><span class="code-lang">${escapeHtml(lang || "code")}</span>` +
    `<button type="button" class="code-copy" data-code-id="${id}">${iconHtml("doc", 12)} copy</button></div>` +
    `<pre><code id="${id}">${escapeHtml(code)}</code></pre></div>`
  );
}

export function renderMarkdown(src: string): string {
  const lines = String(src).split("\n");
  let html = "", i = 0, inList = false, listTag = "";
  const closeList = () => { if (inList) { html += `</${listTag}>`; inList = false; } };
  while (i < lines.length) {
    const line = lines[i];
    const fence = line.match(/^```(\w*)\s*$/);
    if (fence) {
      closeList();
      const buf: string[] = []; i++;
      while (i < lines.length && !/^```\s*$/.test(lines[i])) { buf.push(lines[i]); i++; }
      i++;
      html += codeBlockHtml(fence[1], buf.join("\n"));
      continue;
    }
    const h = line.match(/^(#{1,4})\s+(.*)$/);
    if (h) { closeList(); const n = h[1].length; html += `<h${n} class="md-h">${mdInline(escapeHtml(h[2]))}</h${n}>`; i++; continue; }
    const li = line.match(/^\s*([-*]|\d+\.)\s+(.*)$/);
    if (li) {
      const want = /\d/.test(li[1]) ? "ol" : "ul";
      if (!inList || listTag !== want) { closeList(); listTag = want; inList = true; html += `<${want}>`; }
      html += `<li>${mdInline(escapeHtml(li[2]))}</li>`; i++; continue;
    }
    if (/^\s*$/.test(line)) { closeList(); i++; continue; }
    closeList();
    const para = [line]; i++;
    while (i < lines.length && !/^\s*$/.test(lines[i]) && !/^```/.test(lines[i]) &&
           !/^#{1,4}\s/.test(lines[i]) && !/^\s*([-*]|\d+\.)\s/.test(lines[i])) { para.push(lines[i]); i++; }
    html += `<p>${mdInline(escapeHtml(para.join("\n"))).replace(/\n/g, "<br>")}</p>`;
  }
  closeList();
  return html;
}

function splitThinking(content: string): { think: string; answer: string; thinking?: boolean } {
  let m = content.match(/^\s*<think>([\s\S]*?)<\/think>\s*([\s\S]*)$/);
  if (m) return { think: m[1].trim(), answer: m[2] };
  m = content.match(/^\s*<think>([\s\S]*)$/);
  if (m) return { think: m[1], answer: "", thinking: true };
  return { think: "", answer: content };
}

const typing = `<span class="typing"><i></i><i></i><i></i></span>`;
export function renderAssistant(content: string): string {
  if (!content) return typing;
  const t = splitThinking(content);
  let html = "";
  if (t.think || t.thinking) {
    html +=
      `<details class="think"${t.thinking ? " open" : ""}><summary>${iconHtml("idea", 13)} thinking${t.thinking ? "…" : ""}</summary>` +
      `<div class="think-body">${escapeHtml(t.think)}</div></details>`;
  }
  if (t.answer) html += renderMarkdown(t.answer);
  return html || typing;
}
