// Development-only spacing audit for one rendered dashboard page.
// Paste into the browser console on a fully loaded route (or evaluate it from a
// browser tool). It returns findings for docs/ui-ux-checklist.md "Spacing and rhythm".
;(() => {
  const SCALE = [0, 4, 8, 12, 16, 24, 32, 48]
  const TOL = 1.5
  const findings = []
  const label = (el) => {
    const id = el.id ? `#${el.id}` : ''
    const cls = [...el.classList].slice(0, 2).map((c) => `.${c}`).join('')
    const text = (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 40)
    return `${el.tagName.toLowerCase()}${id}${cls}${text ? ` “${text}”` : ''}`
  }
  const add = (rule, el, detail) => findings.push({ rule, element: label(el), detail })
  const visible = (el) => {
    const s = getComputedStyle(el)
    const r = el.getBoundingClientRect()
    return s.display !== 'none' && s.visibility !== 'hidden' && s.position !== 'absolute' && s.position !== 'fixed' && r.height > 0 && r.width > 0
  }
  const onScale = (v) => SCALE.some((s) => Math.abs(v - s) <= TOL)
  const firstTextRect = (el) => {
    const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT, {
      acceptNode: (n) => (n.textContent.trim() && visible(n.parentElement) ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT),
    })
    const node = walker.nextNode()
    if (!node) return null
    const range = document.createRange()
    range.selectNodeContents(node)
    return range.getClientRects()[0] || null
  }

  const main = document.querySelector('#main-content') || document.querySelector('main')
  if (!main) return [{ rule: 'setup', element: 'document', detail: 'No #main-content or main element' }]
  const inset = main.getBoundingClientRect().left + parseFloat(getComputedStyle(main).paddingLeft)
  // The page root is the first visible block inside main; its children are the top-level blocks.
  let root = main
  while (root.children.length === 1 && visible(root.children[0])) root = root.children[0]
  const blocks = [...root.children].filter(visible)

  // Stacked containers: the page root plus wide page-level wrappers (tab panels,
  // sections), never the inside of cards, dialogs or menus.
  const wide = (el) => el.getBoundingClientRect().width >= main.clientWidth * 0.7
  const containers = [root, ...main.querySelectorAll('[role="tabpanel"], .tab-content, section, .ops-page, form')].filter(
    (el, i, all) => all.indexOf(el) === i && visible(el) && wide(el) && !el.closest('[role="dialog"], .resource-card, .ui-card, .card'),
  )
  const stacked = (parent) => {
    const children = [...parent.children].filter(visible)
    return children.filter((el, i) => i === 0 || el.getBoundingClientRect().top >= children[i - 1].getBoundingClientRect().bottom - 1)
  }
  containers.forEach((container) => {
    const items = stacked(container)
    // 1. Vertical gaps between stacked blocks stay on the scale and never exceed 48px.
    items.slice(1).forEach((el, i) => {
      const prev = items[i].getBoundingClientRect()
      const gap = Math.round((el.getBoundingClientRect().top - prev.bottom) * 10) / 10
      if (!onScale(gap) || gap > 48) add('section-gap', el, `${gap}px after previous block (scale: ${SCALE.join(', ')})`)
      // Visual whitespace includes padding on both sides; more than 56px reads as a hole.
      const padBelow = parseFloat(getComputedStyle(items[i]).paddingBottom)
      const padAbove = parseFloat(getComputedStyle(el).paddingTop)
      const visual = Math.round(gap + padBelow + padAbove)
      if (visual > 56) add('visual-gap', el, `${visual}px of empty space above (gap ${gap} + padding ${padBelow + padAbove})`)
      // 2. Text never touches a rule above it.
      const text = firstTextRect(el)
      if (parseFloat(getComputedStyle(items[i]).borderBottomWidth) > 0 && text && text.top - prev.bottom < 8)
        add('text-touches-rule', el, `${Math.round(text.top - prev.bottom)}px from the divider above`)
    })
  })

  // 3. Content starts on the page inset (label, not padding, aligns for ghost controls).
  blocks.forEach((el) => {
    const r = el.getBoundingClientRect()
    if (r.width >= main.clientWidth - 2) return // full-bleed rows are checked by their own content
    const text = firstTextRect(el)
    if (!text) return
    const offset = Math.round(text.left - inset)
    const own = Math.round(r.left - inset)
    if (Math.abs(own) > 2) add('off-inset', el, `block starts ${own}px from the page inset`)
    else if (offset > 2 && offset < 24 && getComputedStyle(el).borderLeftWidth === '0px' && !el.querySelector('svg'))
      add('label-off-inset', el, `first label starts ${offset}px inside the inset`)
  })

  // 4. Sibling buttons in one row share a height and a top edge.
  const rows = new Set([...document.querySelectorAll('.button')].map((b) => b.parentElement))
  rows.forEach((row) => {
    const buttons = [...row.children].filter((c) => c.classList.contains('button') && visible(c))
    if (buttons.length < 2) return
    const heights = new Set(buttons.map((b) => Math.round(b.getBoundingClientRect().height)))
    const tops = new Set(buttons.map((b) => Math.round(b.getBoundingClientRect().top)))
    if (heights.size > 1) add('button-heights', row, `sibling buttons are ${[...heights].join(' / ')}px tall`)
    if (heights.size === 1 && tops.size > 1 && getComputedStyle(row).flexWrap !== 'wrap')
      add('button-tops', row, `sibling buttons start at different heights`)
  })

  // 5. Page title row: state sits on the title's line, actions align to the title row.
  const h1 = main.querySelector('h1')
  if (h1) {
    const t = h1.getBoundingClientRect()
    const row = h1.parentElement
    ;[...row.children].filter((c) => c !== h1 && visible(c) && c.textContent.trim()).forEach((c) => {
      const r = c.getBoundingClientRect()
      const dy = Math.abs(r.top + r.height / 2 - (t.top + t.height / 2))
      if (dy > 3) add('title-row', c, `${Math.round(dy)}px off the title's centre line`)
    })
  }

  // 6. Cards: equal padding, and metadata never wraps a long code value.
  document.querySelectorAll('.resource-card, .ui-card, .ops-catalog-card, .row-card').forEach((card) => {
    if (!visible(card)) return
    const s = getComputedStyle(card)
    if (Math.abs(parseFloat(s.paddingTop) - parseFloat(s.paddingLeft)) > 1)
      add('card-padding', card, `padding ${s.paddingTop} top vs ${s.paddingLeft} sides`)
    card.querySelectorAll('code').forEach((code) => {
      const lh = parseFloat(getComputedStyle(code).lineHeight) || 18
      if (code.getClientRects().length > 1 || code.getBoundingClientRect().height > lh * 1.5)
        add('wrapped-value', code, 'long value wraps inside card metadata')
    })
  })

  // 8. Section headers sit 12px above their content, and blocks carry no padding holes.
  document.querySelectorAll('.section-toolbar, .app-workspace-section-head').forEach((head) => {
    const next = head.nextElementSibling
    if (!visible(head) || !next || !visible(next)) return
    const gap = Math.round(next.getBoundingClientRect().top - head.getBoundingClientRect().bottom)
    if (gap > 12) add('header-to-content', head, `${gap}px between the section header and its content (want 8)`)
  })
  containers.forEach((container) =>
    stacked(container).forEach((el) => {
      const s = getComputedStyle(el)
      const worst = Math.max(parseFloat(s.paddingTop), parseFloat(s.paddingBottom))
      if (worst > 24 && !el.matches('.resource-card, .ui-card, .card, [role="dialog"]'))
        add('padding-hole', el, `${worst}px of vertical padding inside the block`)
    }),
  )

  // 7. Document never scrolls sideways.
  if (document.documentElement.scrollWidth > innerWidth + 1)
    add('overflow', document.documentElement, `document is ${document.documentElement.scrollWidth - innerWidth}px wider than the viewport`)
  return findings
})()
