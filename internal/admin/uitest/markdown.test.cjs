const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const context = { window: {}, URL };
vm.runInNewContext(fs.readFileSync(require('node:path').join(__dirname, '../ui/markdown.js'), 'utf8'), context);
const markdown = context.window.EngramMarkdown;

test('renders headings lists and code while preserving source content', () => {
  const output = markdown.render('# 标题\n\n**说明**\n\n- 第一步\n- 第二步\n\n```sh\necho "<hello>"\n```');
  assert.match(output, /<h1>标题<\/h1>/);
  assert.match(output, /<strong>说明<\/strong>/);
  assert.match(output, /<ul>/);
  assert.match(output, /echo &quot;&lt;hello&gt;&quot;/);
  assert.match(output, /btn-copy-code/);
});

test('untrusted HTML and URL protocols stay inert in previews and diffs', () => {
  for (const input of ['<img src=x onerror=alert(1)>', '<script>alert(1)</script>', '[点我](javascript:alert)', '[点我](data:text/html,x)', '[点我](jAvAsCrIpT:alert)']) {
    const output = markdown.render(input);
    assert.doesNotMatch(output, /<img|<script|href="(?:javascript|data):/i);
  }
  assert.match(markdown.render('[文档](https://example.com/a?q=1&b=2)'), /href="https:\/\/example.com\/a\?q=1&amp;b=2"/);
  assert.doesNotMatch(markdown.diff('', '<img onerror=alert(1)>'), /<img/);
});

test('diff identifies changed lines without counting common prefix or suffix as edits', () => {
  const output = markdown.diff('same\nold\nend', 'same\nnew\nend');
  assert.match(output, /diff-removed">− old/);
  assert.match(output, /diff-added">\+ new/);
  assert.doesNotMatch(output, /diff-(?:removed|added)">[−+] (?:same|end)/);
  assert.match(markdown.diff('same', 'same'), /正文无变化/);
});

test('unfinished headings and unterminated fences remain readable', () => {
  assert.doesNotThrow(() => markdown.render('# Valid\n\n## \n\nBody'));
  assert.match(markdown.render('# Valid\n\n## \n\nBody'), /<p>Body<\/p>/);
  assert.match(markdown.render('```js\n<unfinished>'), /&lt;unfinished&gt;/);
});
