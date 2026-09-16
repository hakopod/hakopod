# Source-build setup

New source builds use four steps in the shared self-hosted and Cloud dashboard:

1. **Repository:** name the application and service, choose a Git connection and repository, then select the source branch. A repository subdirectory is optional. Connection management opens in a separate tab to keep the draft.
2. **Build recipe:** detect the framework, explicitly apply the suggestion, or choose a Dockerfile, framework recipe or Cloud Native Buildpacks. Review the target architecture. Public build values and CI secret references remain available in optional sections.
3. **Runtime:** choose image defaults or override the command and arguments, configure environment variables (including paste/import `.env`), and choose resources and public HTTP access. Registry credentials default to automatic matching; custom credentials remain available. Existing application builds preserve their resource and networking settings.
4. **Review:** inspect the repository, recipe, resources, runtime command, registry and automation. Environment values remain hidden. Saving opens the existing workflow review; installing that workflow is a separate action, and saving does not deploy a service.

Back and step navigation retain the draft in memory, including unimported `.env` text. A browser reload or closing the page discards the unsaved draft; credentials are not persisted to browser storage. Existing build edits retain the full configuration form.

Native constraints are checked before moving forward. Explicit API field paths return new builds to the appropriate step and focus the invalid control. Permission, network and capacity failures remain banners with the draft retained. Build APIs identify repository, branch, paths, port, size, architecture and framework fields; the UI recognizes the `invalid input:` wrapper without guessing from prose.

Cloud's edition hook identifies hosted Free compute. Build, image and template forms explain the one-application limit and link to the existing application when its slot is occupied. Unsupported resource/architecture choices are labeled and disabled, adding services is capped at one, and replicas are limited to one. Preview and shared-network creation explain their BYO requirement. Stateless templates do not show a persistent-disk input. Imported or existing advanced configuration is kept verbatim and explained before review; the backend remains authoritative for all deployment paths.

Hosted Free permits one small AMD64 service, including a private background worker, with outbound HTTP/HTTPS. Scheduled jobs, persistent storage, extra services, larger profiles and advanced networking require BYO compute. Native application secrets remain supported. These presentation rules do not remove BYO or self-hosted capabilities.
