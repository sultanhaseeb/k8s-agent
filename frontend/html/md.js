// Minimal markdown renderer for agent replies.
//
// Everything is built as DOM nodes with textContent — innerHTML is never used.
// The input is model output describing cluster objects, so it must be treated
// as untrusted: any HTML in it ends up as visible text, not markup.
//
// Supports: fenced code, inline code, bold, italic, strikethrough, headings,
// bullet/numbered lists, pipe tables, blockquotes, and horizontal rules.

(function (global) {
  const FENCE = /^\s*```/;
  const HEADING = /^(#{1,6})\s+(.*)$/;
  const RULE = /^\s*([-*_])\s*(?:\1\s*){2,}$/;
  const QUOTE = /^\s*>\s?/;
  const BULLET = /^\s*[-*+]\s+/;
  const NUMBERED = /^\s*\d+[.)]\s+/;
  const TABLE_SEP = /^\s*\|?[\s:|-]*-[\s:|-]*\|?\s*$/;

  // Order matters: code spans are matched first so ** inside them stays literal.
  const INLINE = /(`[^`\n]+`)|(\*\*[^*\n]+\*\*)|(__[^_\n]+__)|(~~[^~\n]+~~)|(\*[^*\n]+\*)|(_[^_\n]+_)/g;

  function inline(text, parent) {
    let last = 0;
    let match;
    INLINE.lastIndex = 0;
    while ((match = INLINE.exec(text)) !== null) {
      if (match.index > last) {
        parent.appendChild(document.createTextNode(text.slice(last, match.index)));
      }
      const token = match[0];
      let el;
      if (token.startsWith("`")) {
        el = document.createElement("code");
        el.textContent = token.slice(1, -1);
      } else if (token.startsWith("**") || token.startsWith("__")) {
        el = document.createElement("strong");
        el.textContent = token.slice(2, -2);
      } else if (token.startsWith("~~")) {
        el = document.createElement("del");
        el.textContent = token.slice(2, -2);
      } else {
        el = document.createElement("em");
        el.textContent = token.slice(1, -1);
      }
      parent.appendChild(el);
      last = INLINE.lastIndex;
    }
    if (last < text.length) {
      parent.appendChild(document.createTextNode(text.slice(last)));
    }
    return parent;
  }

  // Split a pipe-table row, dropping the empty cells either side of the outer pipes.
  function splitRow(line) {
    const cells = line.trim().replace(/^\|/, "").replace(/\|$/, "").split("|");
    return cells.map((c) => c.trim());
  }

  function isBlockStart(line) {
    return (
      !line.trim() ||
      FENCE.test(line) ||
      HEADING.test(line) ||
      RULE.test(line) ||
      QUOTE.test(line) ||
      BULLET.test(line) ||
      NUMBERED.test(line)
    );
  }

  function renderMarkdown(src) {
    const frag = document.createDocumentFragment();
    const lines = String(src == null ? "" : src).replace(/\r\n?/g, "\n").split("\n");
    let i = 0;

    while (i < lines.length) {
      const line = lines[i];

      if (!line.trim()) {
        i += 1;
        continue;
      }

      // Fenced code block.
      if (FENCE.test(line)) {
        const body = [];
        i += 1;
        while (i < lines.length && !FENCE.test(lines[i])) {
          body.push(lines[i]);
          i += 1;
        }
        i += 1; // closing fence, if present
        const pre = document.createElement("pre");
        const code = document.createElement("code");
        code.textContent = body.join("\n");
        pre.appendChild(code);
        frag.appendChild(pre);
        continue;
      }

      // Pipe table: a header row followed by a |---|---| separator.
      if (line.includes("|") && i + 1 < lines.length && TABLE_SEP.test(lines[i + 1])) {
        const table = document.createElement("table");
        const thead = document.createElement("thead");
        const headRow = document.createElement("tr");
        for (const cell of splitRow(line)) {
          headRow.appendChild(inline(cell, document.createElement("th")));
        }
        thead.appendChild(headRow);
        table.appendChild(thead);

        const tbody = document.createElement("tbody");
        i += 2;
        while (i < lines.length && lines[i].includes("|") && lines[i].trim()) {
          const tr = document.createElement("tr");
          for (const cell of splitRow(lines[i])) {
            tr.appendChild(inline(cell, document.createElement("td")));
          }
          tbody.appendChild(tr);
          i += 1;
        }
        table.appendChild(tbody);

        const wrap = document.createElement("div");
        wrap.className = "md-table";
        wrap.appendChild(table);
        frag.appendChild(wrap);
        continue;
      }

      // Heading.
      const heading = HEADING.exec(line);
      if (heading) {
        const level = Math.min(heading[1].length + 2, 6); // h1 -> h3, keep it modest
        frag.appendChild(inline(heading[2], document.createElement("h" + level)));
        i += 1;
        continue;
      }

      // Horizontal rule.
      if (RULE.test(line)) {
        frag.appendChild(document.createElement("hr"));
        i += 1;
        continue;
      }

      // Blockquote.
      if (QUOTE.test(line)) {
        const body = [];
        while (i < lines.length && QUOTE.test(lines[i])) {
          body.push(lines[i].replace(QUOTE, ""));
          i += 1;
        }
        const quote = document.createElement("blockquote");
        inline(body.join("\n"), quote);
        frag.appendChild(quote);
        continue;
      }

      // Lists.
      if (BULLET.test(line) || NUMBERED.test(line)) {
        const ordered = NUMBERED.test(line) && !BULLET.test(line);
        const list = document.createElement(ordered ? "ol" : "ul");
        while (i < lines.length && (BULLET.test(lines[i]) || NUMBERED.test(lines[i]))) {
          const item = lines[i].replace(ordered ? NUMBERED : BULLET, "");
          list.appendChild(inline(item, document.createElement("li")));
          i += 1;
        }
        frag.appendChild(list);
        continue;
      }

      // Paragraph: consecutive plain lines, single newlines kept as breaks.
      const para = document.createElement("p");
      let first = true;
      while (i < lines.length && !isBlockStart(lines[i])) {
        if (!first) para.appendChild(document.createElement("br"));
        inline(lines[i], para);
        first = false;
        i += 1;
      }
      frag.appendChild(para);
    }

    return frag;
  }

  global.renderMarkdown = renderMarkdown;
})(window);
