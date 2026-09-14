import { dashboardEdition } from '../lib/dashboard-edition'

export const brandLabel = ['Hakopod', dashboardEdition.brandSuffix, dashboardEdition.releaseChannel]
  .filter(Boolean)
  .join(' ')

// The same edition identity appears on account pages and inside the dashboard.
export function Brand({ icon = false }: { icon?: boolean }) {
  return (
    <>
      {icon ? (
        <span className="hako-product-mark">
          <img className="hako-brand-symbol" src="/favicon.svg" alt="" width="24" height="24" />
          <span>Hakopod</span>
        </span>
      ) : (
        <>
          <img
            className="hako-wordmark-dark"
            src="/brand/hakopod-horizontal-paper.svg"
            alt=""
            width="124"
          />
          <img
            className="hako-wordmark-light"
            src="/brand/hakopod-horizontal-ink.svg"
            alt=""
            width="124"
          />
        </>
      )}
      {dashboardEdition.brandSuffix && (
        <span className="hako-edition-brand">
          <span>{dashboardEdition.brandSuffix}</span>
          {dashboardEdition.releaseChannel && (
            <span className="hako-release-badge">{dashboardEdition.releaseChannel}</span>
          )}
        </span>
      )}
    </>
  )
}
