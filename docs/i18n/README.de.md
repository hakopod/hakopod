# Hakopod

[English](../../README.md) · [简体中文](README.zh.md) · [日本語](README.ja.md) · [Español](README.es.md) · [Português](README.pt-BR.md) · Deutsch

Deine Apps, deine Regeln. Hakopod ist eine Open-Source-Plattform, mit der du Anwendungen auf deinen eigenen Servern deployst und betreibst: per Git-Push, mit einer importierten `docker-compose.yml` oder aus einer Vorlage. Du prüfst, was sich ändert, und deployst dann mit TLS auf deine Domain.

```sh
curl -fsSL https://hakopod.com/scripts/installer.sh | sudo sh
```

Zum Start reicht ein frischer Ubuntu- oder Debian-Server mit 4 GiB RAM. Hakopod ist noch Alpha; lies [was bereits verifiziert ist und was nicht](../milestones.md) (Englisch), bevor du Produktivlast darauf legst. Website und Anleitungen: [hakopod.com](https://hakopod.com).

## Wofür du nicht mehr zahlen musst

Jede Zeile ist eine Vorlage aus dem [Katalog](../templates.md), die du schon heute aus dem Dashboard deployen kannst. Der Katalog hält Verifizierungsstatus und Grenzen jeder Vorlage fest; schau dort nach, bevor du dich auf eine verlässt.

| Statt | Betreibe |
| --- | --- |
| Vercel, Heroku, Render | Deine eigenen Apps, aus Git, einem Dockerfile oder Compose |
| Sentry | GlitchTip |
| Google Analytics | Umami |
| Zapier | n8n |
| Pingdom, Better Stack | Uptime Kuma |
| Airtable | NocoDB |
| Einen geteilten Passwortmanager | Vaultwarden |
| ChatGPT-Team-Lizenzen | Open WebUI mit deinen eigenen Modell-Keys |
| Gemanagtes Postgres und Redis | PostgreSQL, Valkey, Redis |

## Warum Hakopod

- **Unten Kubernetes, oben kein YAML.** Hakopod läuft vom ersten Server an auf K3s. Eine zweite Maschine hinzuzufügen heißt, einen Worker anzumelden, nicht zu migrieren. Du beschreibst die Anwendung und ihre Services, Hakopod schreibt die Manifeste.
- **Sieh die Änderung, bevor sie live geht.** Jedes Deployment beginnt als Plan, der zeigt, was sich ändert. Ein Rollback erzeugt eine neue, nachvollziehbare Revision.
- **Bring deine Compose-Datei mit.** Füge eine `docker-compose.yml` ein, prüfe die umgewandelte Konfiguration und den Plan, und deploye.
- **Gib Agenten einen Schlüssel, nicht deinen Cluster.** CLI und MCP-Endpunkt nutzen eingeschränkte, kurzlebige Maschinenschlüssel. CI und Agenten brauchen nie Kubernetes-Zugangsdaten.
- **Standardmäßig privat.** Services teilen sich ein privates Netzwerk; nur was du als öffentlich markierst, bekommt eine Adresse.

## Vergleich

Coolify, Dokploy und Openship sind gute Werkzeuge, und wer heute die ausgereifteste Option sucht, findet bei Coolify die größte Community. So unterscheiden sich die Ansätze laut der jeweiligen Projektdokumentation im Oktober 2026:

| | Hakopod | Coolify | Dokploy | Openship |
| --- | --- | --- | --- | --- |
| Apps laufen auf | K3s (Kubernetes) | Docker | Docker, Swarm für mehrere Server | Docker, K3s für Cluster |
| Geschrieben in | Go | PHP (Laravel) | TypeScript | TypeScript |

K3s braucht Arbeitsspeicher, deshalb verlangt Hakopod mehr als Docker-basierte Werkzeuge. Dafür übernimmt Kubernetes die Verteilung auf mehrere Server und die Rolling Updates.

## Wie wir es bauen

Wir entwickeln Hakopod mit Hilfe von KI-Coding-Agenten; ihre Anweisungen stehen in [AGENTS.md](../../AGENTS.md). Jedes Release führt vor der Veröffentlichung den Installer auf frischen amd64- und arm64-GitHub-Runnern aus. Wenn etwas kaputtgeht, [öffne ein Issue](https://github.com/hakopod/hakopod/issues/new) mit Version, Betriebssystem und dem, was du versucht hast (gern auf Deutsch).

Die vollständige Dokumentation (Englisch) steht im [README](../../README.md).
