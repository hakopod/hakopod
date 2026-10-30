# Hakopod

[English](../../README.md) · [简体中文](README.zh.md) · [日本語](README.ja.md) · [Español](README.es.md) · Português · [Deutsch](README.de.md)

Seus apps, suas regras. Hakopod é uma plataforma de código aberto para fazer deploy e rodar suas aplicações em servidores que são seus: faça push pelo Git, importe um `docker-compose.yml` ou escolha um template, revise o que vai mudar e publique com TLS no seu domínio.

```sh
curl -fsSL https://hakopod.com/scripts/installer.sh | sudo sh
```

Um servidor Ubuntu ou Debian novo com 4 GiB de RAM é suficiente para começar. Hakopod está em alpha; antes de colocar algo em produção, leia [o que já foi verificado e o que ainda não foi](../milestones.md) (em inglês). Site e guias: [hakopod.com](https://hakopod.com).

## O que você pode parar de pagar

Cada linha é um template do [catálogo](../templates.md) que você já pode implantar pelo painel. O catálogo registra o status de verificação e os limites de cada template, então confira antes de depender de algum.

| Em vez de | Rode |
| --- | --- |
| Vercel, Heroku, Render | Seus próprios apps, a partir de Git, Dockerfile ou Compose |
| Sentry | GlitchTip |
| Google Analytics | Umami |
| Zapier | n8n |
| Pingdom, Better Stack | Uptime Kuma |
| Airtable | NocoDB |
| Um gerenciador de senhas compartilhado | Vaultwarden |
| Assentos do ChatGPT para equipes | Open WebUI com suas próprias chaves de modelo |
| Postgres e Redis gerenciados | PostgreSQL, Valkey, Redis |

## Por que Hakopod

- **Kubernetes por baixo, nada de YAML por cima.** Hakopod roda em K3s desde o primeiro servidor, então crescer para uma segunda máquina é registrar um worker, não migrar. Você descreve a aplicação e seus serviços; o Hakopod escreve os manifestos.
- **Veja a mudança antes de ela ir ao ar.** Todo deploy começa como um plano mostrando o que vai mudar. Um rollback cria uma revisão nova e auditável.
- **Traga seu arquivo Compose.** Cole um `docker-compose.yml`, revise a configuração convertida e o plano, e faça o deploy.
- **Dê uma chave aos seus agentes, não o cluster.** A CLI e o endpoint MCP usam chaves de máquina com escopo e vida curta. CI e agentes nunca precisam de credenciais do Kubernetes.
- **Privado por padrão.** Os serviços compartilham uma rede privada; só os que você marca como públicos recebem um endereço.

## Comparação

Coolify, Dokploy e Openship são boas ferramentas, e se você quer a opção mais madura hoje, o Coolify tem a maior comunidade. Estas são as diferenças segundo a documentação de cada projeto em outubro de 2026:

| | Hakopod | Coolify | Dokploy | Openship |
| --- | --- | --- | --- | --- |
| Roda os apps em | K3s (Kubernetes) | Docker | Docker, Swarm para vários servidores | Docker, K3s para clusters |
| Escrito em | Go | PHP (Laravel) | TypeScript | TypeScript |
| Mostra o que vai mudar antes de cada deploy | Sim, um plano por deploy | — | — | Etapa de revisão no assistente de deploy |
| Ambientes de preview | Manual, a partir de uma imagem construída, 1–72 horas | Por pull request | Por pull request (GitHub) | A partir de um branch Git |
| Listas de IPs permitidos e bloqueados, limites de taxa, regras por país | No painel | — | — | Pela CLI |
| Restringir o painel e a API às suas redes ou a uma VPN | Lista de permitidos, firewall, servidor WireGuard integrado, Tailscale, OpenVPN, IPsec (Pro) | Lista de IPs permitidos na API | — | — |
| Passkeys / TOTP | Sim / Sim | Não / Sim | Sim / Sim | Sim / Sim |
| Escalar apps ociosos para zero | Sim | — | — | Sim |
| MCP para agentes de IA | Sim | Sim | Sim | Sim |
| Provedor oficial do Terraform | Sim | — | Só da comunidade | — |
| Bancos de dados gerenciados | PostgreSQL, MySQL, MongoDB, Redis, ClickHouse, Vitess, Oracle Free | PostgreSQL, MySQL, MariaDB, MongoDB, Redis, ClickHouse | PostgreSQL, MySQL, MariaDB, MongoDB, Redis, libSQL | PostgreSQL, MySQL, MongoDB, Redis |
| Templates prontos para deploy | 57 | 300+ | 500+ | 42 |
| Log de auditoria | Sim; exportar o histórico é Pro | Sim | Enterprise | Sim |
| Licença | Apache-2.0 | Apache-2.0 | Apache-2.0, mais um diretório proprietário | Apache-2.0 |

"—" significa que não encontramos menção a isso na documentação do projeto, não que seja impossível lá. Avise se alguma célula estiver desatualizada.

O K3s usa memória, por isso o Hakopod pede mais do que ferramentas baseadas em Docker. Em troca, o agendamento entre servidores e as atualizações graduais vêm do Kubernetes.

## Como construímos

Desenvolvemos o Hakopod com a ajuda de agentes de programação com IA; as instruções deles estão em [AGENTS.md](../../AGENTS.md). Cada versão roda o instalador em runners novos do GitHub, amd64 e arm64, antes de ser publicada. Se algo quebrar, [abra uma issue](https://github.com/hakopod/hakopod/issues/new) com sua versão, sistema operacional e o que você tentou (pode escrever em português).

A documentação completa (em inglês) está no [README](../../README.md).
