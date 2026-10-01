export function DatabaseStack({
  x,
  y,
  primary,
  ready,
  selected,
  unknown,
}: {
  x: number
  y: number
  primary: boolean
  ready: boolean
  selected: boolean
  unknown: boolean
}) {
  return (
    <g
      transform={`translate(${x} ${y})`}
      className={`db-stack ${primary ? 'db-stack-primary' : unknown ? 'db-stack-unknown' : ''} ${selected ? 'db-stack-selected' : ''}`}
      aria-hidden="true"
    >
      <ellipse className="db-stack-shadow" cy="40" rx="58" ry="28" />
      <ellipse className="db-stack-ring" cy="34" rx="55" ry="27" />
      {[20, 0, -20].map((top) => (
        <g key={top} transform={`translate(0 ${top})`}>
          <path
            className="db-stack-side"
            d="M-43,-15 A43,21 0 0 0 43,-15 V4 A43,21 0 0 1 -43,4 Z"
          />
          <ellipse className="db-stack-top" cy="-15" rx="43" ry="21" />
          <path className="db-stack-highlight" d="M-43,4 A43,21 0 0 0 43,4" />
          <circle
            className={ready ? 'db-stack-led' : 'db-stack-led-off'}
            cx="-23"
            cy="13"
            r="2.5"
          />
        </g>
      ))}
      <ellipse className="db-stack-core" cy="-35" rx="18" ry="9" />
    </g>
  )
}
