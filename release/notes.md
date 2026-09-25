Hakopod 0.1.0-alpha.31 adds CPU and memory sizing for Managed Actions runner pools.

## Runner resource sizing

Set CPU and memory reservations and limits per runner slot in **Catalog → Automation → Managed Actions**. The form shows total reservations for the selected concurrency and preserves existing inherited or custom settings when editing a pool.

When a pool cannot fit, the review screen now shows requested and available CPU and memory with the exact shortage. Scheduling depends on reserved capacity, so free host memory alone does not mean a pool has enough CPU. Capacity accounting also includes restartable Docker sidecars alongside application containers.

Smaller CPU reservations allow runners to share CPU; concurrent builds still compete with applications for host resources. This release does not change existing pool sizes, admission rules, Pro access or sandbox isolation.

## Upgrade

```sh
sudo sh installer.sh --upgrade --version 0.1.0-alpha.31
```

Direct upgrades are supported from alpha.29 and alpha.30, the last two published releases. Older installations must upgrade through a supported intermediate release. Managed Actions still requires Pro access and the optional sandbox module installed from a verified release kit.

## Validation

The complete engine Go suite passed with isolated PostgreSQL. Both self-hosted and composed Cloud dashboards passed typechecks, builds and tests. Capacity regressions cover CPU and memory shortages, smaller reservations, negative free capacity, native sidecars and init-container peaks. Independent UI review covered both editions and themes, desktop and narrow mobile layouts, keyboard/touch interactions, inherited and partial settings, and inline capacity errors.

Publication is gated by artifact verification and native fresh-install/upgrade acceptance, with 12 host cases across managed, local and external database modes. Live GitHub workflow execution with a resized pool has not been exercised.
