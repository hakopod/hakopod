import type { ImgHTMLAttributes } from 'react'

// This is the user-supplied Slack image asset. Text beside it names the integration.
export function SlackLogo({ className, ...props }: ImgHTMLAttributes<HTMLImageElement>) {
  return (
    <img
      alt=""
      aria-hidden="true"
      className={className}
      draggable={false}
      height={1200}
      src="/brand/slack-new.jpg"
      width={1200}
      {...props}
    />
  )
}
