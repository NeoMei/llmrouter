# Homepage Domestic Model Positioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite the default homepage around the domestic models actually enabled on llmrouter.quukk.com and the customer flow of obtaining an API key, selecting a model, and making requests.

**Architecture:** Centralize the production-facing model families, conservative service facts, and terminal examples in a pure TypeScript content contract. Existing React sections consume that contract while all new marketing copy remains behind i18next keys synchronized across the seven supported locales.

**Tech Stack:** React 19, TypeScript, TanStack Router, i18next/react-i18next, Node test runner through `tsx`, Bun.

## Global Constraints

- The primary homepage promise is unified access to domestic AI models.
- Featured model families are exactly DeepSeek, GLM, Kimi, Qwen, Doubao, and MiniMax.
- Terminal tabs are exactly DeepSeek, GLM, Kimi, and Qwen and use enabled production model identifiers through `/v1/chat/completions`.
- Preserve the authenticated `Get API Key` route, unauthenticated registration route, `/docs`, `/pricing`, and the approved Claude Code, Codex, and OpenCode links.
- Preserve the exact approved Chinese sentence `支持一键配置并完美适配 LLM API 多协议配置。`.
- Preserve protected new-api and QuantumNous identifiers, attribution, metadata, and copyright headers.
- Do not modify or commit `scripts/prepare-sidecar-env.py`.
- Locale JSON files may only be written by `web/scripts/add-missing-keys.mjs`, followed by `bun run i18n:sync`.
- Publication and production deployment are out of scope until separately requested.

---

## File structure

- Create `web/src/features/home/home-content.ts`: domestic model, service-fact, and terminal-demo contract.
- Create `web/src/features/home/__tests__/positioning.test.ts`: pure contract regression coverage.
- Modify `web/src/features/home/__tests__/copy.test.ts`: locale coverage for the new positioning and preserved LLM API sentence.
- Modify `web/src/features/home/components/hero-terminal-demo.tsx`: render the centralized domestic-model demos.
- Modify `web/src/features/home/components/sections/hero.tsx`: domestic-model value proposition.
- Modify `web/src/features/home/components/sections/stats.tsx`: current-service facts.
- Modify `web/src/features/home/components/sections/features.tsx`: domestic-model features and pills.
- Modify `web/src/features/home/components/sections/how-it-works.tsx`: customer onboarding flow.
- Modify `web/src/features/home/components/sections/cta.tsx`: hosted-service call to action.
- Temporarily create then delete `web/scripts/add-missing-keys.mjs`: atomically write translations.
- Modify `web/src/i18n/locales/{en,zh,zh-TW,fr,ja,ru,vi}.json` through the sanctioned script only.

### Task 1: Lock the domestic-model content contract

**Files:**
- Create: `web/src/features/home/__tests__/positioning.test.ts`
- Create: `web/src/features/home/home-content.ts`

**Interfaces:**
- Produces `FEATURED_DOMESTIC_MODELS`, `HOME_STATS`, `ApiDemoConfig`, and `HOME_API_DEMOS`.
- `HOME_STATS` items expose `end`, `suffix`, and `labelKey`.
- `HOME_API_DEMOS` items expose the rendering fields already used by `HeroTerminalDemo` plus an explicit domestic model identifier in each request.

- [ ] **Step 1: Write the failing contract test**

Create a Node test that imports the four exports and asserts:

```ts
assert.deepEqual(FEATURED_DOMESTIC_MODELS, [
  'DeepSeek',
  'GLM',
  'Kimi',
  'Qwen',
  'Doubao',
  'MiniMax',
])

assert.deepEqual(HOME_STATS, [
  { end: 20, suffix: '+', labelKey: 'domestic models available' },
  { end: 6, suffix: '+', labelKey: 'domestic model brands' },
  { end: 3, suffix: '', labelKey: 'coding clients supported' },
  { end: 1, suffix: '', labelKey: 'unified API key' },
])

assert.deepEqual(
  HOME_API_DEMOS.map((demo) => ({
    label: demo.label,
    endpoint: demo.endpoint,
    modelLine: demo.request[0],
  })),
  [
    { label: 'DeepSeek', endpoint: '/v1/chat/completions', modelLine: '"model": "deepseek-chat",' },
    { label: 'GLM', endpoint: '/v1/chat/completions', modelLine: '"model": "glm-5",' },
    { label: 'Kimi', endpoint: '/v1/chat/completions', modelLine: '"model": "kimi-k2.7-code",' },
    { label: 'Qwen', endpoint: '/v1/chat/completions', modelLine: '"model": "Qwen3.7-Max-A",' },
  ]
)
```

- [ ] **Step 2: Verify RED**

Run from `web/`:

```bash
bun run test src/features/home/__tests__/positioning.test.ts
```

Expected: FAIL because `../home-content` does not exist.

- [ ] **Step 3: Implement the pure content module**

Create `home-content.ts` with the project copyright header and this content:

```ts
export type AccentTone = 'emerald' | 'amber' | 'blue' | 'violet'

export interface ApiDemoConfig {
  id: string
  label: string
  method: 'POST' | 'GET'
  endpoint: string
  headers: string[]
  request: string[]
  response: string[]
  responseHighlights: string[]
  tokens: number
  latency: number
  accent: AccentTone
}

export const FEATURED_DOMESTIC_MODELS = [
  'DeepSeek',
  'GLM',
  'Kimi',
  'Qwen',
  'Doubao',
  'MiniMax',
] as const

export const HOME_STATS = [
  { end: 20, suffix: '+', labelKey: 'domestic models available' },
  { end: 6, suffix: '+', labelKey: 'domestic model brands' },
  { end: 3, suffix: '', labelKey: 'coding clients supported' },
  { end: 1, suffix: '', labelKey: 'unified API key' },
] as const

const CHAT_RESPONSE = [
  '{',
  '  "choices": [{ "message": { "content": <text> } }],',
  '  "usage": { "total_tokens": <tokens> }',
  '}',
]

const createChatDemo = (
  id: string,
  label: string,
  model: string,
  accent: AccentTone,
  tokens: number,
  latency: number
): ApiDemoConfig => ({
  id,
  label,
  method: 'POST',
  endpoint: '/v1/chat/completions',
  headers: ['"Authorization: Bearer sk-••••"'],
  request: [
    `"model": "${model}",`,
    '"messages": [',
    '  { "role": "user", "content": "..." }',
    ']',
  ],
  response: CHAT_RESPONSE,
  responseHighlights: ['<text>', '<tokens>'],
  tokens,
  latency,
  accent,
})

export const HOME_API_DEMOS = [
  createChatDemo('deepseek', 'DeepSeek', 'deepseek-chat', 'emerald', 27, 142),
  createChatDemo('glm', 'GLM', 'glm-5', 'amber', 31, 168),
  createChatDemo('kimi', 'Kimi', 'kimi-k2.7-code', 'blue', 29, 156),
  createChatDemo('qwen', 'Qwen', 'Qwen3.7-Max-A', 'violet', 25, 93),
] as const satisfies readonly ApiDemoConfig[]
```

- [ ] **Step 4: Verify GREEN**

Run the focused positioning test again and expect all assertions to pass.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/home/home-content.ts web/src/features/home/__tests__/positioning.test.ts
git commit -m "test(home): lock domestic model positioning"
```

### Task 2: Rewrite every default-homepage message

**Files:**
- Modify: `web/src/features/home/__tests__/copy.test.ts`
- Modify: `web/src/features/home/components/hero-terminal-demo.tsx`
- Modify: `web/src/features/home/components/sections/hero.tsx`
- Modify: `web/src/features/home/components/sections/stats.tsx`
- Modify: `web/src/features/home/components/sections/features.tsx`
- Modify: `web/src/features/home/components/sections/how-it-works.tsx`
- Modify: `web/src/features/home/components/sections/cta.tsx`

**Interfaces:**
- Consumes `FEATURED_DOMESTIC_MODELS`, `HOME_STATS`, and `HOME_API_DEMOS` from `home-content.ts`.
- Keeps existing section component props and exports unchanged.

- [ ] **Step 1: Extend the locale-copy test before implementation**

Require every locale to define these English source keys:

```ts
const POSITIONING_KEYS = [
  'Domestic AI Model Access Service',
  'One API for',
  'Leading Domestic AI Models',
  'Access DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more through one API. Use domestic models directly in your AI applications and coding tools.',
  'domestic models available',
  'domestic model brands',
  'coding clients supported',
  'unified API key',
  'Domestic Model Access',
  'One API connects DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more.',
  'Unified Routing',
  'Route requests across domestic model services with consistent access and failover controls',
  'Built for domestic AI applications,',
  'ready for production workflows',
  'Usage Visibility',
  'Track model usage, costs, and request performance in one place',
  'Three steps to use domestic models',
  'Create API Key',
  'Sign up and create an API key with the access scope you need',
  'Choose a Domestic Model',
  'Select DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, or another enabled domestic model',
  'Connect and Monitor',
  'Call the unified API from your application or coding tool and track usage and costs',
  'Ready to use',
  'domestic AI models?',
  'Create an API key, choose an enabled domestic model, and start calling it from your application or coding tool.',
  'View Models',
] as const
```

For `zh`, assert the hero keys render as `国产大模型接入服务`, `一个 API，接入`, and `主流国产大模型`. Keep the existing assertion for the exact LLM API multi-protocol sentence.

- [ ] **Step 2: Verify RED**

Run the copy test and expect failure because the new keys are missing.

- [ ] **Step 3: Update components with minimal structural change**

Make these exact substitutions while preserving surrounding JSX and styles:

```tsx
// hero.tsx
{t('Domestic AI Model Access Service')}
{t('One API for')}
{t('Leading Domestic AI Models')}
{t(
  'Access DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more through one API. Use domestic models directly in your AI applications and coding tools.'
)}
```

```ts
// hero-terminal-demo.tsx
import {
  HOME_API_DEMOS,
  type AccentTone,
  type ApiDemoConfig,
} from '../home-content'

// Delete the local AccentTone, ApiDemoConfig, and API_DEMOS declarations.
// Replace API_DEMOS references with HOME_API_DEMOS.
```

```tsx
// stats.tsx
import { HOME_STATS } from '../../home-content'

const stats: StatItem[] = HOME_STATS.map((stat) => ({
  ...stat,
  label: t(stat.labelKey),
}))
```

```tsx
// features.tsx
import { FEATURED_DOMESTIC_MODELS } from '../../home-content'

// First visual:
{FEATURED_DOMESTIC_MODELS.map((name) => /* preserve existing pill JSX */)}

// Replace the four affected text calls:
title: t('Domestic Model Access')
desc: t('One API connects DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more.')
title: t('Unified Routing')
desc: t('Route requests across domestic model services with consistent access and failover controls')
title: t('Usage Visibility')
desc: t('Track model usage, costs, and request performance in one place')
{t('Built for domestic AI applications,')}
{t('ready for production workflows')}
```

```tsx
// how-it-works.tsx steps and heading
{
  num: '1',
  title: t('Create API Key'),
  desc: t('Sign up and create an API key with the access scope you need'),
}
{
  num: '2',
  title: t('Choose a Domestic Model'),
  desc: t('Select DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, or another enabled domestic model'),
}
{
  num: '3',
  title: t('Connect and Monitor'),
  desc: t('Call the unified API from your application or coding tool and track usage and costs'),
}
{t('Three steps to use domestic models')}
```

```tsx
// cta.tsx
{t('Ready to use')}
{t('domestic AI models?')}
{t(
  'Create an API key, choose an enabled domestic model, and start calling it from your application or coding tool.'
)}
{t('View Models')}
```

- [ ] **Step 4: Apply complete translations with the sanctioned script**

Create `web/scripts/add-missing-keys.mjs` using the exact writer structure from `.agents/skills/i18n-translate/SKILL.md` and this exact translation contract:

```js
const newKeys = {
  en: Object.fromEntries(POSITIONING_KEYS.map((key) => [key, key])),
  zh: {
    'Domestic AI Model Access Service': '国产大模型接入服务',
    'One API for': '一个 API，接入',
    'Leading Domestic AI Models': '主流国产大模型',
    'Access DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more through one API. Use domestic models directly in your AI applications and coding tools.': '通过一个 API 统一接入 DeepSeek、智谱 GLM、Kimi、通义千问、豆包、MiniMax 等国内模型，让 AI 应用和开发工具直接使用国产模型能力。',
    'domestic models available': '可用国产模型',
    'domestic model brands': '国产模型品牌',
    'coding clients supported': '支持开发工具',
    'unified API key': '统一 API Key',
    'Domestic Model Access': '国产模型统一接入',
    'One API connects DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more.': '一个 API 统一接入 DeepSeek、智谱 GLM、Kimi、通义千问、豆包、MiniMax 等国内模型。',
    'Unified Routing': '统一路由',
    'Route requests across domestic model services with consistent access and failover controls': '在多个国内模型服务间统一调度，并提供稳定接入和故障切换能力',
    'Built for domestic AI applications,': '为国产 AI 应用打造，',
    'ready for production workflows': '服务真实生产场景',
    'Usage Visibility': '用量清晰可见',
    'Track model usage, costs, and request performance in one place': '在一个平台查看模型用量、费用和请求性能',
    'Three steps to use domestic models': '三步接入国产模型',
    'Create API Key': '创建 API Key',
    'Sign up and create an API key with the access scope you need': '注册并创建 API Key，按需设置访问范围',
    'Choose a Domestic Model': '选择国产模型',
    'Select DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, or another enabled domestic model': '选择 DeepSeek、智谱 GLM、Kimi、通义千问、豆包、MiniMax 或其他已启用国产模型',
    'Connect and Monitor': '接入并监控',
    'Call the unified API from your application or coding tool and track usage and costs': '从应用或开发工具调用统一 API，并跟踪用量和费用',
    'Ready to use': '准备开始使用',
    'domestic AI models?': '国产大模型了吗？',
    'Create an API key, choose an enabled domestic model, and start calling it from your application or coding tool.': '创建 API Key，选择已启用的国产模型，即可从应用或开发工具开始调用。',
    'View Models': '查看模型',
  },
  'zh-TW': {
    'Domestic AI Model Access Service': '國產大型模型接入服務',
    'One API for': '一個 API，接入',
    'Leading Domestic AI Models': '主流國產大型模型',
    'Access DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more through one API. Use domestic models directly in your AI applications and coding tools.': '透過一個 API 統一接入 DeepSeek、智譜 GLM、Kimi、通義千問、豆包、MiniMax 等國內模型，讓 AI 應用與開發工具直接使用國產模型能力。',
    'domestic models available': '可用國產模型',
    'domestic model brands': '國產模型品牌',
    'coding clients supported': '支援開發工具',
    'unified API key': '統一 API Key',
    'Domestic Model Access': '國產模型統一接入',
    'One API connects DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more.': '一個 API 統一接入 DeepSeek、智譜 GLM、Kimi、通義千問、豆包、MiniMax 等國內模型。',
    'Unified Routing': '統一路由',
    'Route requests across domestic model services with consistent access and failover controls': '在多個國內模型服務間統一調度，並提供穩定接入與容錯切換能力',
    'Built for domestic AI applications,': '為國產 AI 應用打造，',
    'ready for production workflows': '服務真實生產場景',
    'Usage Visibility': '用量清晰可見',
    'Track model usage, costs, and request performance in one place': '在一個平台查看模型用量、費用與請求效能',
    'Three steps to use domestic models': '三步接入國產模型',
    'Create API Key': '建立 API Key',
    'Sign up and create an API key with the access scope you need': '註冊並建立 API Key，按需設定存取範圍',
    'Choose a Domestic Model': '選擇國產模型',
    'Select DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, or another enabled domestic model': '選擇 DeepSeek、智譜 GLM、Kimi、通義千問、豆包、MiniMax 或其他已啟用國產模型',
    'Connect and Monitor': '接入並監控',
    'Call the unified API from your application or coding tool and track usage and costs': '從應用或開發工具呼叫統一 API，並追蹤用量與費用',
    'Ready to use': '準備開始使用',
    'domestic AI models?': '國產大型模型了嗎？',
    'Create an API key, choose an enabled domestic model, and start calling it from your application or coding tool.': '建立 API Key，選擇已啟用的國產模型，即可從應用或開發工具開始呼叫。',
    'View Models': '查看模型',
  },
  fr: {
    'Domestic AI Model Access Service': "Accès aux modèles d’IA chinois",
    'One API for': 'Une API pour',
    'Leading Domestic AI Models': "les principaux modèles d’IA chinois",
    'Access DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more through one API. Use domestic models directly in your AI applications and coding tools.': "Accédez à DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax et plus encore via une seule API. Utilisez directement ces modèles dans vos applications et outils de développement.",
    'domestic models available': 'modèles chinois disponibles',
    'domestic model brands': 'familles de modèles chinois',
    'coding clients supported': 'outils de développement pris en charge',
    'unified API key': 'clé API unifiée',
    'Domestic Model Access': 'Accès unifié aux modèles chinois',
    'One API connects DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more.': 'Une API relie DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax et bien plus.',
    'Unified Routing': 'Routage unifié',
    'Route requests across domestic model services with consistent access and failover controls': "Acheminez les requêtes entre les services de modèles chinois avec un accès cohérent et un contrôle de basculement",
    'Built for domestic AI applications,': "Conçu pour les applications d’IA chinoises,",
    'ready for production workflows': 'prêt pour les usages en production',
    'Usage Visibility': "Visibilité de l’utilisation",
    'Track model usage, costs, and request performance in one place': "Suivez l’utilisation des modèles, les coûts et les performances au même endroit",
    'Three steps to use domestic models': 'Trois étapes pour utiliser les modèles chinois',
    'Create API Key': 'Créer une clé API',
    'Sign up and create an API key with the access scope you need': "Inscrivez-vous et créez une clé API avec les droits d’accès nécessaires",
    'Choose a Domestic Model': 'Choisir un modèle chinois',
    'Select DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, or another enabled domestic model': 'Sélectionnez DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax ou un autre modèle chinois activé',
    'Connect and Monitor': 'Connecter et suivre',
    'Call the unified API from your application or coding tool and track usage and costs': "Appelez l’API unifiée depuis votre application ou outil de développement et suivez l’utilisation et les coûts",
    'Ready to use': 'Prêt à utiliser',
    'domestic AI models?': "les modèles d’IA chinois ?",
    'Create an API key, choose an enabled domestic model, and start calling it from your application or coding tool.': "Créez une clé API, choisissez un modèle chinois activé et commencez à l’appeler depuis votre application ou outil de développement.",
    'View Models': 'Voir les modèles',
  },
  ja: {
    'Domestic AI Model Access Service': '中国製AIモデル接続サービス',
    'One API for': '1つのAPIで',
    'Leading Domestic AI Models': '主要な中国製AIモデルへ接続',
    'Access DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more through one API. Use domestic models directly in your AI applications and coding tools.': '1つのAPIでDeepSeek、GLM、Kimi、Qwen、Doubao、MiniMaxなどに接続し、AIアプリや開発ツールから中国製モデルを直接利用できます。',
    'domestic models available': '利用可能な中国製モデル',
    'domestic model brands': '中国製モデルブランド',
    'coding clients supported': '対応開発ツール',
    'unified API key': '統一APIキー',
    'Domestic Model Access': '中国製モデルへの統一接続',
    'One API connects DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more.': '1つのAPIでDeepSeek、GLM、Kimi、Qwen、Doubao、MiniMaxなどに接続します。',
    'Unified Routing': '統一ルーティング',
    'Route requests across domestic model services with consistent access and failover controls': '中国製モデルサービス間でリクエストを統一的に振り分け、安定した接続とフェイルオーバーを提供します',
    'Built for domestic AI applications,': '中国製AIモデルを使うアプリ向けに設計し、',
    'ready for production workflows': '本番ワークフローに対応',
    'Usage Visibility': '利用状況の可視化',
    'Track model usage, costs, and request performance in one place': 'モデル利用量、コスト、リクエスト性能を一か所で確認できます',
    'Three steps to use domestic models': '3ステップで中国製モデルを利用',
    'Create API Key': 'APIキーを作成',
    'Sign up and create an API key with the access scope you need': '登録して、必要なアクセス範囲のAPIキーを作成します',
    'Choose a Domestic Model': '中国製モデルを選択',
    'Select DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, or another enabled domestic model': 'DeepSeek、GLM、Kimi、Qwen、Doubao、MiniMax、または有効な中国製モデルを選択します',
    'Connect and Monitor': '接続して監視',
    'Call the unified API from your application or coding tool and track usage and costs': 'アプリや開発ツールから統一APIを呼び出し、利用量とコストを確認します',
    'Ready to use': '使ってみませんか',
    'domestic AI models?': '中国製AIモデルを？',
    'Create an API key, choose an enabled domestic model, and start calling it from your application or coding tool.': 'APIキーを作成し、有効な中国製モデルを選んで、アプリや開発ツールから呼び出せます。',
    'View Models': 'モデルを見る',
  },
  ru: {
    'Domestic AI Model Access Service': 'Доступ к китайским ИИ-моделям',
    'One API for': 'Один API для',
    'Leading Domestic AI Models': 'ведущих китайских ИИ-моделей',
    'Access DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more through one API. Use domestic models directly in your AI applications and coding tools.': 'Подключайте DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax и другие модели через единый API. Используйте китайские модели напрямую в приложениях и инструментах разработки.',
    'domestic models available': 'доступных китайских моделей',
    'domestic model brands': 'семейств китайских моделей',
    'coding clients supported': 'поддерживаемых инструментов разработки',
    'unified API key': 'единый API-ключ',
    'Domestic Model Access': 'Единый доступ к китайским моделям',
    'One API connects DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more.': 'Один API подключает DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax и другие модели.',
    'Unified Routing': 'Единая маршрутизация',
    'Route requests across domestic model services with consistent access and failover controls': 'Распределяйте запросы между сервисами китайских моделей с единым доступом и управляемым переключением',
    'Built for domestic AI applications,': 'Создано для приложений с китайскими ИИ-моделями,',
    'ready for production workflows': 'готово к рабочим нагрузкам',
    'Usage Visibility': 'Прозрачное использование',
    'Track model usage, costs, and request performance in one place': 'Отслеживайте использование моделей, расходы и производительность запросов в одном месте',
    'Three steps to use domestic models': 'Три шага для работы с китайскими моделями',
    'Create API Key': 'Создать API-ключ',
    'Sign up and create an API key with the access scope you need': 'Зарегистрируйтесь и создайте API-ключ с нужными правами доступа',
    'Choose a Domestic Model': 'Выберите китайскую модель',
    'Select DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, or another enabled domestic model': 'Выберите DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax или другую доступную китайскую модель',
    'Connect and Monitor': 'Подключайтесь и контролируйте',
    'Call the unified API from your application or coding tool and track usage and costs': 'Вызывайте единый API из приложения или инструмента разработки и отслеживайте использование и расходы',
    'Ready to use': 'Готовы использовать',
    'domestic AI models?': 'китайские ИИ-модели?',
    'Create an API key, choose an enabled domestic model, and start calling it from your application or coding tool.': 'Создайте API-ключ, выберите доступную китайскую модель и вызывайте её из приложения или инструмента разработки.',
    'View Models': 'Посмотреть модели',
  },
  vi: {
    'Domestic AI Model Access Service': 'Dịch vụ truy cập mô hình AI Trung Quốc',
    'One API for': 'Một API cho',
    'Leading Domestic AI Models': 'các mô hình AI Trung Quốc hàng đầu',
    'Access DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more through one API. Use domestic models directly in your AI applications and coding tools.': 'Truy cập DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax và nhiều mô hình khác qua một API. Dùng trực tiếp các mô hình Trung Quốc trong ứng dụng AI và công cụ lập trình.',
    'domestic models available': 'mô hình Trung Quốc khả dụng',
    'domestic model brands': 'dòng mô hình Trung Quốc',
    'coding clients supported': 'công cụ lập trình được hỗ trợ',
    'unified API key': 'API Key thống nhất',
    'Domestic Model Access': 'Truy cập thống nhất mô hình Trung Quốc',
    'One API connects DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, and more.': 'Một API kết nối DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax và nhiều mô hình khác.',
    'Unified Routing': 'Định tuyến thống nhất',
    'Route requests across domestic model services with consistent access and failover controls': 'Định tuyến yêu cầu giữa các dịch vụ mô hình Trung Quốc với khả năng truy cập nhất quán và chuyển đổi dự phòng',
    'Built for domestic AI applications,': 'Được xây dựng cho ứng dụng AI Trung Quốc,',
    'ready for production workflows': 'sẵn sàng cho quy trình thực tế',
    'Usage Visibility': 'Theo dõi sử dụng rõ ràng',
    'Track model usage, costs, and request performance in one place': 'Theo dõi mức dùng mô hình, chi phí và hiệu năng yêu cầu tại một nơi',
    'Three steps to use domestic models': 'Ba bước để dùng mô hình Trung Quốc',
    'Create API Key': 'Tạo API Key',
    'Sign up and create an API key with the access scope you need': 'Đăng ký và tạo API Key với phạm vi truy cập cần thiết',
    'Choose a Domestic Model': 'Chọn mô hình Trung Quốc',
    'Select DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax, or another enabled domestic model': 'Chọn DeepSeek, GLM, Kimi, Qwen, Doubao, MiniMax hoặc một mô hình Trung Quốc khác đang bật',
    'Connect and Monitor': 'Kết nối và theo dõi',
    'Call the unified API from your application or coding tool and track usage and costs': 'Gọi API thống nhất từ ứng dụng hoặc công cụ lập trình và theo dõi mức dùng cùng chi phí',
    'Ready to use': 'Sẵn sàng sử dụng',
    'domestic AI models?': 'các mô hình AI Trung Quốc?',
    'Create an API key, choose an enabled domestic model, and start calling it from your application or coding tool.': 'Tạo API Key, chọn một mô hình Trung Quốc đang bật và bắt đầu gọi từ ứng dụng hoặc công cụ lập trình.',
    'View Models': 'Xem mô hình',
  },
}
```

In the temporary script, define `POSITIONING_KEYS` from the keys of the `zh` object before `newKeys` so the `en` expression is executable. Preserve model names and API identifiers in English. Run:

```bash
cd web
node scripts/add-missing-keys.mjs
bun run i18n:sync
```

Delete the temporary script with `apply_patch` after synchronization.

- [ ] **Step 5: Verify GREEN**

Run:

```bash
bun run test src/features/home/__tests__/positioning.test.ts src/features/home/__tests__/copy.test.ts src/features/home/__tests__/navigation.test.ts
```

Expected: all homepage tests pass.

- [ ] **Step 6: Commit**

Stage only the homepage and locale files, verify the temporary script is absent, and commit:

```bash
git commit -m "feat(home): focus homepage on domestic models"
```

### Task 3: Verify the complete homepage rewrite

**Files:**
- Verify all files modified by Tasks 1 and 2.

- [ ] **Step 1: Run static verification**

From `web/` run:

```bash
bun run typecheck
bunx oxlint src/features/home/home-content.ts src/features/home/__tests__/positioning.test.ts src/features/home/__tests__/copy.test.ts src/features/home/components/hero-terminal-demo.tsx src/features/home/components/sections/hero.tsx src/features/home/components/sections/stats.tsx src/features/home/components/sections/features.tsx src/features/home/components/sections/how-it-works.tsx src/features/home/components/sections/cta.tsx
bun run format:check
bun run build
```

- [ ] **Step 2: Audit visible-copy regressions**

Search the default homepage files and confirm OpenAI, Claude, Gemini, and Llama do not appear as supplied model examples. Claude Code, Codex, and OpenCode remain only in the approved supported-client links. Confirm no self-deployment or upstream-channel onboarding copy remains in the default homepage sections.

- [ ] **Step 3: Verify in a local browser**

Run the local production build or development server, open the default homepage in Chinese, and confirm the hero, terminal tabs, service facts, feature grid, three-step flow, and final CTA all show the approved domestic-model positioning at desktop and mobile widths.

- [ ] **Step 4: Review repository scope**

Run `git status --short` and `git diff --stat`. Confirm `scripts/prepare-sidecar-env.py` remains untracked and no unrelated files are staged or committed.
