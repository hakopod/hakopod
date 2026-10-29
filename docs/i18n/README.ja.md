# Hakopod

[English](../../README.md) · [简体中文](README.zh.md) · 日本語 · [Español](README.es.md) · [Português](README.pt-BR.md) · [Deutsch](README.de.md)

あなたのアプリを、あなたのルールで。Hakopod は、自分のサーバーでアプリをデプロイ・運用するためのオープンソースプラットフォームです。Git からの push、`docker-compose.yml` のインポート、テンプレートの選択のいずれかで始め、変更内容を確認してから、自分のドメインに TLS 付きでデプロイします。

```sh
curl -fsSL https://hakopod.com/scripts/installer.sh | sudo sh
```

メモリ 4 GiB の新しい Ubuntu または Debian サーバーが 1 台あれば始められます。Hakopod は現在 alpha 版です。本番環境で使う前に、[検証済みの項目と未検証の項目](../milestones.md)（英語）を確認してください。サイトとガイド：[hakopod.com](https://hakopod.com)

## 払わなくてよくなるサービス

各行は、ダッシュボードから今すぐデプロイできる[テンプレートカタログ](../templates.md)の項目です。カタログには各テンプレートの検証状況と制限が記録されているので、使う前に確認してください。

| 代わりに | 動かすもの |
| --- | --- |
| Vercel、Heroku、Render | 自分のアプリ（Git、Dockerfile、Compose から） |
| Sentry | GlitchTip |
| Google Analytics | Umami |
| Zapier | n8n |
| Pingdom、Better Stack | Uptime Kuma |
| Airtable | NocoDB |
| チーム用パスワード管理 | Vaultwarden |
| ChatGPT のチーム席 | Open WebUI（自分のモデル API キーで） |
| マネージド Postgres と Redis | PostgreSQL、Valkey、Redis |

## Hakopod を選ぶ理由

- **中身は Kubernetes、YAML は書かない。** Hakopod は最初のサーバーから K3s 上で動くので、2 台目を増やすときは worker を登録するだけで、移行は不要です。あなたはアプリとサービスを記述するだけで、マニフェストは Hakopod が書きます。
- **出す前に変更が見える。** すべてのデプロイは、何が変わるかを示すプランから始まります。ロールバックは監査可能な新しいリビジョンとして作られます。
- **Compose ファイルをそのまま。** `docker-compose.yml` を貼り付け、変換後の設定とプランを確認してからデプロイします。
- **エージェントに渡すのは鍵だけ、クラスタではなく。** CLI と MCP エンドポイントは、スコープ付きで短命なマシンキーを使います。CI もエージェントも Kubernetes の認証情報を必要としません。
- **デフォルトで非公開。** サービスはプライベートネットワークを共有し、公開と指定したものだけがアドレスを持ちます。

## 比較

Coolify、Dokploy、Openship はどれも良いツールで、今いちばん成熟した選択肢が欲しいなら、コミュニティが最も大きいのは Coolify です。以下は 2026 年 10 月時点の各プロジェクト公式ドキュメントに基づく違いです。

| | Hakopod | Coolify | Dokploy | Openship |
| --- | --- | --- | --- | --- |
| アプリの実行基盤 | K3s（Kubernetes） | Docker | Docker、複数サーバーは Swarm | Docker、クラスタは K3s |
| 実装言語 | Go | PHP（Laravel） | TypeScript | TypeScript |

K3s はメモリを使うため、Hakopod は Docker ベースのツールより多くのメモリを必要とします。その代わり、サーバー間のスケジューリングとローリングアップデートは Kubernetes が担います。

## 開発の進め方

Hakopod は AI コーディングエージェントの助けを借りて開発しています。その指示は [AGENTS.md](../../AGENTS.md) にあります。各リリースは公開前に、新しい amd64 と arm64 の GitHub runner でインストーラーを実行しています。問題があれば、バージョン、OS、試したことを書いて [issue を作成](https://github.com/hakopod/hakopod/issues/new)してください（日本語で構いません）。

詳しいドキュメント（英語）は [README](../../README.md) をご覧ください。
