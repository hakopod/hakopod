import { memo, useMemo, type ReactNode } from 'react'
import { tokenizeTOML } from '../lib/toml-highlight'

export const TOMLCode = memo(function TOMLCode({
  code,
  className,
}: {
  code: string
  className?: string
}) {
  const content = useMemo(() => {
    const nodes: ReactNode[] = []
    let end = 0
    for (const token of tokenizeTOML(code)) {
      if (token.start > end) nodes.push(code.slice(end, token.start))
      nodes.push(
        <span className={`toml-${token.kind}`} key={token.start}>
          {code.slice(token.start, token.end)}
        </span>,
      )
      end = token.end
    }
    // Keep the complete remainder in one text node after a lexer limit.
    if (end < code.length) nodes.push(code.slice(end))
    return nodes
  }, [code])
  return (
    <pre
      className={`toml-code${className ? ` ${className}` : ''}`}
      tabIndex={0}
      aria-label="TOML configuration"
    >
      <code>{content}</code>
    </pre>
  )
})
