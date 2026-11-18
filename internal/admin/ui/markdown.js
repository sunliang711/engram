(function () {
  // escapeHTML keeps memory content inert before inserting the supported Markdown subset.
  function escapeHTML(value) {
    return String(value == null ? "" : value).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }

  // inline renders text formatting and allows only safe, explicit link protocols.
  function inline(text) {
    const pattern = /`([^`]+)`|\[([^\]]+)\]\(([^\s)]+)\)|\*\*([^*]+)\*\*|\*([^*]+)\*/g;
    let output = "";
    let cursor = 0;
    for (const match of text.matchAll(pattern)) {
      output += escapeHTML(text.slice(cursor, match.index));
      if (match[1] !== undefined) output += `<code>${escapeHTML(match[1])}</code>`;
      else if (match[2] !== undefined) {
        let safe = false;
        try { safe = ["http:", "https:", "mailto:"].includes(new URL(match[3]).protocol); } catch (_) { /* Invalid links remain readable text. */ }
        output += safe ? `<a href="${escapeHTML(match[3])}" target="_blank" rel="noopener noreferrer">${escapeHTML(match[2])}</a>` : escapeHTML(match[2]);
      } else if (match[4] !== undefined) output += `<strong>${escapeHTML(match[4])}</strong>`;
      else output += `<em>${escapeHTML(match[5])}</em>`;
      cursor = match.index + match[0].length;
    }
    return output + escapeHTML(text.slice(cursor));
  }

  // render produces a readable preview without executing raw HTML, images, or embedded content.
  function render(source) {
    const lines = String(source || "").replace(/\r\n?/g, "\n").split("\n");
    const output = [];
    let paragraph = [];
    let list = "";
    // flush closes the current paragraph and list before starting another block.
    function flush() {
      if (paragraph.length) { output.push(`<p>${paragraph.map(inline).join("<br>")}</p>`); paragraph = []; }
      if (list) { output.push(`</${list}>`); list = ""; }
    }
    for (let index = 0; index < lines.length; index++) {
      const line = lines[index];
      const fence = line.match(/^\s*(`{3,}|~{3,})(.*)$/);
      const heading = line.match(/^(#{1,6})[ \t]+(.*)$/);
      if (fence) {
        flush();
        const code = [];
        while (++index < lines.length && !new RegExp(`^\\s*${fence[1][0]}{${fence[1].length},}\\s*$`).test(lines[index])) code.push(lines[index]);
        output.push(`<div class="markdown-code"><div class="code-header"><span>${escapeHTML(fence[2].trim() || "代码")}</span><button type="button" class="secondary small btn-copy-code">复制代码</button></div><pre><code>${escapeHTML(code.join("\n"))}</code></pre></div>`);
      } else if (/^\s*$/.test(line)) flush();
      else if (heading) {
        flush(); const level = heading[1].length;
        output.push(`<h${level}>${inline(heading[2])}</h${level}>`);
      } else if (/^\s*([-*_])\1\1[\s*_\-]*$/.test(line)) { flush(); output.push("<hr>"); }
      else if (/^\s*>\s?/.test(line)) { flush(); output.push(`<blockquote>${inline(line.replace(/^\s*>\s?/, ""))}</blockquote>`); }
      else if (/^\s*([-+*]|\d+\.)\s+/.test(line)) {
        const item = line.match(/^\s*([-+*]|\d+\.)\s+(.*)$/); const type = /\d/.test(item[1]) ? "ol" : "ul";
        if (paragraph.length || (list && list !== type)) flush();
        if (!list) { output.push(`<${type}>`); list = type; }
        output.push(`<li>${inline(item[2])}</li>`);
      } else { if (list) flush(); paragraph.push(line); }
    }
    flush();
    return output.join("\n") || '<p class="empty-state">暂无正文</p>';
  }

  // diff shows the changed line region, preserving shared prefix and suffix as context.
  function diff(previous, current) {
    const before = String(previous || "").split("\n");
    const after = String(current || "").split("\n");
    let start = 0;
    while (start < before.length && start < after.length && before[start] === after[start]) start++;
    if (start === before.length && start === after.length) return '<p class="field-hint">正文无变化</p>';
    let endBefore = before.length, endAfter = after.length;
    while (endBefore > start && endAfter > start && before[endBefore - 1] === after[endAfter - 1]) { endBefore--; endAfter--; }
    return '<pre class="revision-diff">' + before.slice(Math.max(0, start - 2), start).map(line => `<span>  ${escapeHTML(line)}</span>`).join("") + before.slice(start, endBefore).map(line => `<span class="diff-removed">− ${escapeHTML(line)}</span>`).join("") + after.slice(start, endAfter).map(line => `<span class="diff-added">+ ${escapeHTML(line)}</span>`).join("") + after.slice(endAfter, endAfter + 2).map(line => `<span>  ${escapeHTML(line)}</span>`).join("") + '</pre>';
  }

  window.EngramMarkdown = { render, diff };
})();
