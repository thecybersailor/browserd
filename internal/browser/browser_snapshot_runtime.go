package browser

// Generated from browser-snapshot BotWorks browser-snapshot runtime sha256:fe0e6c1c48fc423c195506d2d63dc6184c75b1b71c1334974519c415436e9b0e; DO NOT EDIT.
const browserSnapshotRuntimeScript = `(() => {
  const captureSnapshotTree = function captureSnapshotTree(limits = { nodes: 12000, depth: 160, text: 4000 }) {
    const refs = {};
    const omissions = [];
    let sequence = 0;
    let refSequence = 0;
    let visited = 0;
    const omit = (nodeId, reason) => {
        if (!omissions.some(item => item.nodeId === nodeId && item.reason === reason))
            omissions.push({ nodeId, reason });
    };
    const relevant = /^(id|class|role|title|lang|dir|href|src|alt|for|headers|scope|name|type|accept|placeholder|contenteditable|tabindex|colspan|rowspan|aria-.+)$/;
    const skipped = new Set(['head', 'script', 'style', 'noscript', 'template']);
    const actionTags = new Set(['a', 'button', 'input', 'textarea', 'select', 'area', 'summary']);
    const walk = (dom, selector, depth, parentID, hidden, inSelect = false) => {
        if (++visited > limits.nodes) {
            omit(parentID, 'node-limit');
            return null;
        }
        if (depth > limits.depth) {
            omit(parentID, 'depth-limit');
            return null;
        }
        if (dom.nodeType === 3) {
            if (hidden || !dom.textContent)
                return null;
            const parentStyle = dom.parentElement ? getComputedStyle(dom.parentElement) : null;
            if (parentStyle?.visibility === 'hidden' || parentStyle?.visibility === 'collapse')
                return null;
            const range = document.createRange();
            range.selectNodeContents(dom);
            if (!inSelect && !Array.from(range.getClientRects()).some(rect => rect.width > 0 && rect.height > 0))
                return null;
            const whiteSpace = parentStyle?.whiteSpace || '';
            const text = /^(pre|pre-wrap|break-spaces)$/.test(whiteSpace) ? dom.textContent : dom.textContent.replace(/[\t\r\n\f ]+/g, ' ');
            if (!text)
                return null;
            const node = { id: ` + "`" + `n${++sequence}` + "`" + `, tag: '#text', text: text.slice(0, limits.text) };
            if (text.length > limits.text)
                omit(node.id, 'text-limit');
            return node;
        }
        if (dom.nodeType !== 1)
            return null;
        const el = dom;
        const tag = el.tagName.toLowerCase();
        if (skipped.has(tag))
            return null;
        const style = getComputedStyle(el);
        const type = (el.getAttribute('type') || '').toLowerCase();
        const fileInput = tag === 'input' && type === 'file';
        const displayHidden = hidden || style.display === 'none';
        // File inputs remain addressable even inside hidden upload widgets.
        if (displayHidden && !fileInput && !el.querySelector('input[type="file"]'))
            return null;
        const invisible = displayHidden || style.visibility === 'hidden' || style.visibility === 'collapse' || (style.opacity === '0' && tag !== 'img');
        const node = { id: ` + "`" + `n${++sequence}` + "`" + `, tag };
        const attrs = {};
        for (const attr of Array.from(el.attributes)) {
            if (relevant.test(attr.name))
                attrs[attr.name] = attr.value;
        }
        if (tag === 'a' || tag === 'area')
            attrs.href = el.href || attrs.href || '';
        if (tag === 'img') {
            const img = el;
            attrs.src = img.currentSrc || img.src || attrs.src || '';
        }
        if (tag === 'iframe') {
            attrs.src = el.src || attrs.src || '';
            omit(node.id, 'frame');
        }
        if (el.shadowRoot)
            omit(node.id, 'shadow-root');
        if (Object.keys(attrs).length)
            node.attrs = attrs;
        const state = {};
        const disabled = el.matches(':disabled') || el.getAttribute('aria-disabled') === 'true' || el.hasAttribute('inert');
        if (disabled)
            state.disabled = true;
        if (invisible)
            state.hidden = true;
        if (['input', 'textarea', 'select'].includes(tag)) {
            state.value = type === 'password' ? '' : el.value || '';
            if (el.hasAttribute('readonly'))
                state.readOnly = true;
            if (el.hasAttribute('required'))
                state.required = true;
            if (type === 'checkbox' || type === 'radio')
                state.checked = el.checked;
            if (tag === 'select')
                state.selected = Array.from(el.selectedOptions).map(option => option.value);
        }
        if (tag === 'option') {
            state.selected = el.selected;
            state.value = el.value;
        }
        if (tag === 'details')
            state.open = el.open;
        if (el.isContentEditable) {
            state.editable = true;
            const placeholder = el.getAttribute('data-placeholder');
            if (placeholder && !attrs.placeholder) {
                attrs.placeholder = placeholder;
                node.attrs = attrs;
            }
        }
        if (Object.keys(state).length)
            node.state = state;
        const rect = el.getBoundingClientRect();
        const laidOut = rect.width > 0 && rect.height > 0;
        const actionable = actionTags.has(tag) || el.hasAttribute('role') || el.hasAttribute('tabindex') || el.isContentEditable;
        if (actionable && !disabled && ((!invisible && laidOut) || fileInput) && type !== 'hidden') {
            node.ref = ` + "`" + `e${++refSequence}` + "`" + `;
            refs[node.ref] = { ref: node.ref, kind: 'element', tagName: el.tagName, selector, role: attrs.role || '', name: attrs['aria-label'] || attrs.title || attrs.placeholder || '' };
        }
        const children = [];
        let elementIndex = 0;
        for (const child of Array.from(dom.childNodes)) {
            if (visited >= limits.nodes) {
                omit(node.id, 'node-limit');
                break;
            }
            const childSelector = child.nodeType === 1 ? ` + "`" + `${selector} > :nth-child(${++elementIndex})` + "`" + ` : selector;
            // Unlike display/opacity, visibility can be overridden by a descendant.
            const subtreeHidden = displayHidden || (style.opacity === '0' && tag !== 'img');
            const captured = walk(child, childSelector, depth + 1, node.id, subtreeHidden, inSelect || tag === 'select');
            if (captured)
                children.push(captured);
        }
        if (children.length)
            node.children = children;
        const boundary = omissions.some(item => item.nodeId === node.id);
        if (!children.length && !node.ref && !node.attrs && !node.state && !boundary && tag !== 'html' && tag !== 'body' && tag !== 'br')
            return null;
        // Only attribute-free neutral unary wrappers are lossless to compress.
        if ((tag === 'div' || tag === 'span') && el.attributes.length === 0 && !node.ref && !node.state && !boundary && children.length === 1)
            return children[0];
        return node;
    };
    const tree = walk(document.documentElement, 'html', 0, 'n1', false) || { id: 'n1', tag: 'html' };
    const snapshotId = ` + "`" + `snap_${Date.now()}_${Math.random().toString(36).slice(2)}` + "`" + `;
    return { snapshotId, page: { formatVersion: 2, url: location.href, title: document.title, tree, capture: { scope: 'light-dom', complete: omissions.length === 0, omissions } }, refs };
};
  const encodeCompactPage = function encodeCompactPage(page) {
    const attributes = [];
    const states = [];
    const attributeIDs = new Map(), stateIDs = new Map();
    const intern = (value, values, ids) => {
        const key = JSON.stringify(value);
        let id = ids.get(key);
        if (id === undefined) {
            id = values.length;
            values.push(JSON.parse(key));
            ids.set(key, id);
        }
        return id;
    };
    const encode = (node) => {
        if (node.tag === '#text')
            return ['#text', node.id, node.text];
        const properties = {};
        if (node.attrs)
            properties.attrs = intern(node.attrs, attributes, attributeIDs);
        if (node.state)
            properties.state = intern(node.state, states, stateIDs);
        if (node.ref)
            properties.ref = node.ref;
        return [node.tag, node.id, properties, ...(node.children || []).map(encode)];
    };
    const tree = encode(page.tree);
    return { formatVersion: 3, encoding: 'dom-tree-tuples-v1', url: page.url, title: page.title, tree, attributes, states, capture: JSON.parse(JSON.stringify(page.capture)) };
};
  const captured = captureSnapshotTree();
  return { snapshotId: captured.snapshotId, page: encodeCompactPage(captured.page), refs: captured.refs };
})()`
