# Hakopod

[English](../../README.md) · [简体中文](README.zh.md) · [日本語](README.ja.md) · Español · [Português](README.pt-BR.md) · [Deutsch](README.de.md)

Tus apps, tus reglas. Hakopod es una plataforma de código abierto para desplegar y operar tus aplicaciones en servidores propios: haz push desde Git, importa un `docker-compose.yml` o elige una plantilla, revisa qué va a cambiar y despliega con TLS en tu dominio.

```sh
curl -fsSL https://hakopod.com/scripts/installer.sh | sudo sh
```

Para empezar basta un servidor Ubuntu o Debian recién instalado con 4 GiB de RAM. Hakopod está en alpha; antes de confiarle trabajo en producción, lee [qué está verificado y qué no](../milestones.md) (en inglés). Web y guías: [hakopod.com](https://hakopod.com).

## Lo que puedes dejar de pagar

Cada fila es una plantilla del [catálogo](../templates.md) que ya puedes desplegar desde el panel. El catálogo indica el estado de verificación y los límites de cada plantilla, así que revísalo antes de depender de una.

| En lugar de | Ejecuta |
| --- | --- |
| Vercel, Heroku, Render | Tus propias apps, desde Git, un Dockerfile o Compose |
| Sentry | GlitchTip |
| Google Analytics | Umami |
| Zapier | n8n |
| Pingdom, Better Stack | Uptime Kuma |
| Airtable | NocoDB |
| Un gestor de contraseñas compartido | Vaultwarden |
| Licencias de ChatGPT para equipos | Open WebUI con tus propias claves de modelo |
| Postgres y Redis gestionados | PostgreSQL, Valkey, Redis |

## Por qué Hakopod

- **Kubernetes por debajo, nada de YAML por encima.** Hakopod funciona sobre K3s desde el primer servidor, así que crecer a una segunda máquina consiste en registrar un worker, no en migrar. Tú describes la aplicación y sus servicios; Hakopod escribe los manifiestos.
- **Mira el cambio antes de publicarlo.** Cada despliegue empieza como un plan que muestra qué va a cambiar. Un rollback crea una revisión nueva y auditable.
- **Trae tu archivo Compose.** Pega un `docker-compose.yml`, revisa la configuración convertida y el plan, y despliega.
- **Dale una llave a tus agentes, no tu clúster.** La CLI y el endpoint MCP usan claves de máquina con alcance limitado y corta duración. Ni el CI ni los agentes necesitan credenciales de Kubernetes.
- **Privado por defecto.** Los servicios comparten una red privada; solo los que marcas como públicos reciben una dirección.

## Comparación

Coolify, Dokploy y Openship son buenas herramientas, y si hoy quieres la opción más madura, Coolify tiene la comunidad más grande. Estas son las diferencias según la documentación de cada proyecto en octubre de 2026:

| | Hakopod | Coolify | Dokploy | Openship |
| --- | --- | --- | --- | --- |
| Ejecuta las apps sobre | K3s (Kubernetes) | Docker | Docker, Swarm para varios servidores | Docker, K3s para clústeres |
| Escrito en | Go | PHP (Laravel) | TypeScript | TypeScript |

K3s consume memoria, por eso Hakopod pide más que las herramientas basadas en Docker. A cambio, la planificación entre servidores y las actualizaciones progresivas vienen de Kubernetes.

## Cómo lo construimos

Desarrollamos Hakopod con ayuda de agentes de programación con IA; sus instrucciones están en [AGENTS.md](../../AGENTS.md). Cada versión ejecuta el instalador en runners de GitHub amd64 y arm64 nuevos antes de publicarse. Si algo falla, [abre un issue](https://github.com/hakopod/hakopod/issues/new) con tu versión, sistema operativo y lo que intentaste (puedes escribir en español).

La documentación completa (en inglés) está en el [README](../../README.md).
