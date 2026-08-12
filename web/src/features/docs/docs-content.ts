/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
export const DOCS_LANGUAGES = [
  'zhCN',
  'en',
  'fr',
  'ru',
  'ja',
  'vi',
  'zhTW',
] as const

export type DocsLanguage = (typeof DOCS_LANGUAGES)[number]

/**
 * Map the site-wide interface language (i18next code) onto the docs content
 * language. Every interface language has a dedicated content variant; only an
 * unrecognised/missing value falls back to English.
 */
export function docsLanguageForInterface(
  interfaceLanguage: string | undefined
): DocsLanguage {
  const code = interfaceLanguage ?? ''
  return DOCS_LANGUAGES.includes(code as DocsLanguage)
    ? (code as DocsLanguage)
    : 'en'
}

export type DocsLink = {
  href: '/keys' | '/dashboard/models' | '/client-model-routes' | '/install/'
  label: string
}

export type DocsAgent = {
  name:
    | 'Codex CLI/Desktop'
    | 'Claude Code/Desktop'
    | 'OpenCode'
    | 'Continue'
    | 'Aider'
  detail: string
}

export type DocsSection = {
  id:
    | 'start'
    | 'get-token'
    | 'connection-address'
    | 'choose-model'
    | 'configure-agent'
    | 'verify'
    | 'troubleshooting'
  title: string
  body: string
  links: DocsLink[]
  agents?: DocsAgent[]
}

export type DocsContent = {
  title: string
  introduction: string
  sections: DocsSection[]
}

export const docsContent: Record<DocsLanguage, DocsContent> = {
  zhCN: {
    title: '连接 LLMRouter Agent',
    introduction: '使用 Token 将常用 Agent 连接到 LLMRouter。',
    sections: [
      {
        id: 'start',
        title: '开始',
        body: '准备一个 LLMRouter Token、连接地址和要使用的模型名称。',
        links: [],
      },
      {
        id: 'get-token',
        title: '获取 Token',
        body: '登录后创建或管理 Token，并妥善保管它。',
        links: [{ href: '/keys', label: 'Token 管理' }],
      },
      {
        id: 'connection-address',
        title: '连接地址',
        body: '在 Agent 的连接设置中使用 llmrouter.quukk.com 提供的地址。',
        links: [],
      },
      {
        id: 'choose-model',
        title: '选择模型',
        body: '在可用模型中选择适合当前任务的模型；如需设置路由，请在控制台管理。',
        links: [
          { href: '/dashboard/models', label: '查看模型' },
          { href: '/client-model-routes', label: '管理路由' },
        ],
      },
      {
        id: 'configure-agent',
        title: '配置 Agent',
        body: '使用安装脚本或在 Agent 设置中填入 Token、连接地址和模型名称。',
        links: [{ href: '/install/', label: '安装脚本' }],
        agents: [
          { name: 'Codex CLI/Desktop', detail: '按安装脚本完成连接设置。' },
          {
            name: 'Claude Code/Desktop',
            detail:
              'Claude Code 按安装脚本设置；Claude Desktop 使用 MCP tool access。',
          },
          { name: 'OpenCode', detail: '在提供方设置中填写连接信息。' },
          { name: 'Continue', detail: '在模型配置中填写连接信息。' },
          { name: 'Aider', detail: '在启动配置中填写连接信息。' },
        ],
      },
      {
        id: 'verify',
        title: '验证连接',
        body: '在 Agent 中发起一次简短请求；成功返回即表示连接可用。',
        links: [],
      },
      {
        id: 'troubleshooting',
        title: '故障排查',
        body: '检查 Token、连接地址和模型名称是否一致；必要时重新保存配置后再试。',
        links: [],
      },
    ],
  },
  en: {
    title: 'Connect an LLMRouter Agent',
    introduction: 'Use a Token to connect your preferred Agent to LLMRouter.',
    sections: [
      {
        id: 'start',
        title: 'Start',
        body: 'Prepare an LLMRouter Token, connection address, and model name.',
        links: [],
      },
      {
        id: 'get-token',
        title: 'Get a Token',
        body: 'Sign in to create or manage a Token, then keep it private.',
        links: [{ href: '/keys', label: 'Manage Tokens' }],
      },
      {
        id: 'connection-address',
        title: 'Connection address',
        body: 'Use the address from llmrouter.quukk.com in your Agent connection settings.',
        links: [],
      },
      {
        id: 'choose-model',
        title: 'Choose a model',
        body: 'Select a model for the task. Manage routes in the dashboard when needed.',
        links: [
          { href: '/dashboard/models', label: 'View models' },
          { href: '/client-model-routes', label: 'Manage routes' },
        ],
      },
      {
        id: 'configure-agent',
        title: 'Configure an Agent',
        body: 'Use the install scripts or enter your Token, connection address, and model name in the Agent settings.',
        links: [{ href: '/install/', label: 'Install scripts' }],
        agents: [
          {
            name: 'Codex CLI/Desktop',
            detail: 'Follow the install scripts to set up the connection.',
          },
          {
            name: 'Claude Code/Desktop',
            detail:
              'Set up Claude Code with the install scripts; Claude Desktop uses MCP tool access.',
          },
          {
            name: 'OpenCode',
            detail: 'Enter the connection details in provider settings.',
          },
          {
            name: 'Continue',
            detail: 'Enter the connection details in model settings.',
          },
          {
            name: 'Aider',
            detail: 'Enter the connection details in startup configuration.',
          },
        ],
      },
      {
        id: 'verify',
        title: 'Verify',
        body: 'Send a short request in the Agent. A successful response confirms the connection.',
        links: [],
      },
      {
        id: 'troubleshooting',
        title: 'Troubleshooting',
        body: 'Check that the Token, connection address, and model name match; save the settings again if needed.',
        links: [],
      },
    ],
  },
  fr: {
    title: 'Connecter un agent LLMRouter',
    introduction: 'Utilisez un Token pour connecter votre agent à LLMRouter.',
    sections: [
      {
        id: 'start',
        title: 'Démarrage',
        body: 'Préparez un Token LLMRouter, une adresse de connexion et le nom du modèle à utiliser.',
        links: [],
      },
      {
        id: 'get-token',
        title: 'Obtenir un Token',
        body: 'Connectez-vous pour créer ou gérer un Token, puis conservez-le en lieu sûr.',
        links: [{ href: '/keys', label: 'Gérer les Tokens' }],
      },
      {
        id: 'connection-address',
        title: 'Adresse de connexion',
        body: "Utilisez l'adresse fournie par llmrouter.quukk.com dans les paramètres de connexion de votre agent.",
        links: [],
      },
      {
        id: 'choose-model',
        title: 'Choisir un modèle',
        body: 'Sélectionnez un modèle adapté à la tâche. Gérez les routes dans la console si nécessaire.',
        links: [
          { href: '/dashboard/models', label: 'Voir les modèles' },
          { href: '/client-model-routes', label: 'Gérer les routes' },
        ],
      },
      {
        id: 'configure-agent',
        title: 'Configurer un agent',
        body: "Utilisez les scripts d'installation ou saisissez votre Token, l'adresse de connexion et le nom du modèle dans les paramètres de l'agent.",
        links: [{ href: '/install/', label: "Scripts d'installation" }],
        agents: [
          {
            name: 'Codex CLI/Desktop',
            detail:
              "Suivez les scripts d'installation pour configurer la connexion.",
          },
          {
            name: 'Claude Code/Desktop',
            detail:
              "Configurez Claude Code avec les scripts d'installation ; Claude Desktop utilise l'accès aux outils MCP.",
          },
          {
            name: 'OpenCode',
            detail:
              'Saisissez les informations de connexion dans les paramètres du fournisseur.',
          },
          {
            name: 'Continue',
            detail:
              'Saisissez les informations de connexion dans les paramètres du modèle.',
          },
          {
            name: 'Aider',
            detail:
              'Saisissez les informations de connexion dans la configuration de démarrage.',
          },
        ],
      },
      {
        id: 'verify',
        title: 'Vérifier',
        body: "Envoyez une requête courte dans l'agent. Une réponse valide confirme la connexion.",
        links: [],
      },
      {
        id: 'troubleshooting',
        title: 'Dépannage',
        body: "Vérifiez que le Token, l'adresse de connexion et le nom du modèle correspondent ; enregistrez à nouveau les paramètres si nécessaire.",
        links: [],
      },
    ],
  },
  ru: {
    title: 'Подключение агента LLMRouter',
    introduction:
      'Используйте Token для подключения вашего агента к LLMRouter.',
    sections: [
      {
        id: 'start',
        title: 'Начало работы',
        body: 'Подготовьте Token LLMRouter, адрес подключения и имя модели.',
        links: [],
      },
      {
        id: 'get-token',
        title: 'Получить Token',
        body: 'Войдите, чтобы создать или управлять Token, и храните его в безопасности.',
        links: [{ href: '/keys', label: 'Управление Token' }],
      },
      {
        id: 'connection-address',
        title: 'Адрес подключения',
        body: 'Используйте адрес от llmrouter.quukk.com в настройках подключения вашего агента.',
        links: [],
      },
      {
        id: 'choose-model',
        title: 'Выбор модели',
        body: 'Выберите модель для задачи. Управляйте маршрутами в консоли при необходимости.',
        links: [
          { href: '/dashboard/models', label: 'Просмотр моделей' },
          { href: '/client-model-routes', label: 'Управление маршрутами' },
        ],
      },
      {
        id: 'configure-agent',
        title: 'Настройка агента',
        body: 'Используйте скрипты установки или введите Token, адрес подключения и имя модели в настройках агента.',
        links: [{ href: '/install/', label: 'Скрипты установки' }],
        agents: [
          {
            name: 'Codex CLI/Desktop',
            detail: 'Следуйте скриптам установки для настройки подключения.',
          },
          {
            name: 'Claude Code/Desktop',
            detail:
              'Настройте Claude Code через скрипты установки; Claude Desktop использует доступ к инструментам MCP.',
          },
          {
            name: 'OpenCode',
            detail: 'Введите данные подключения в настройках провайдера.',
          },
          {
            name: 'Continue',
            detail: 'Введите данные подключения в настройках модели.',
          },
          {
            name: 'Aider',
            detail: 'Введите данные подключения в конфигурации запуска.',
          },
        ],
      },
      {
        id: 'verify',
        title: 'Проверка',
        body: 'Отправьте короткий запрос в агенте. Успешный ответ подтверждает подключение.',
        links: [],
      },
      {
        id: 'troubleshooting',
        title: 'Устранение неполадок',
        body: 'Проверьте, что Token, адрес подключения и имя модели совпадают; при необходимости сохраните настройки заново.',
        links: [],
      },
    ],
  },
  ja: {
    title: 'LLMRouter エージェントを接続',
    introduction:
      'Token を使って、お使いのエージェントを LLMRouter に接続します。',
    sections: [
      {
        id: 'start',
        title: 'はじめに',
        body: 'LLMRouter Token、接続アドレス、使用するモデル名を用意します。',
        links: [],
      },
      {
        id: 'get-token',
        title: 'Token を取得',
        body: 'サインインして Token を作成・管理し、安全に保管してください。',
        links: [{ href: '/keys', label: 'Token 管理' }],
      },
      {
        id: 'connection-address',
        title: '接続アドレス',
        body: 'エージェントの接続設定で llmrouter.quukk.com のアドレスを使用します。',
        links: [],
      },
      {
        id: 'choose-model',
        title: 'モデルを選択',
        body: 'タスクに適したモデルを選択します。必要に応じてコンソールでルートを管理します。',
        links: [
          { href: '/dashboard/models', label: 'モデルを表示' },
          { href: '/client-model-routes', label: 'ルートを管理' },
        ],
      },
      {
        id: 'configure-agent',
        title: 'エージェントを設定',
        body: 'インストールスクリプトを使用するか、エージェントの設定に Token、接続アドレス、モデル名を入力します。',
        links: [{ href: '/install/', label: 'インストールスクリプト' }],
        agents: [
          {
            name: 'Codex CLI/Desktop',
            detail: 'インストールスクリプトに従って接続を設定します。',
          },
          {
            name: 'Claude Code/Desktop',
            detail:
              'Claude Code はインストールスクリプトで設定します。Claude Desktop は MCP ツールアクセスを使用します。',
          },
          {
            name: 'OpenCode',
            detail: 'プロバイダー設定に接続情報を入力します。',
          },
          {
            name: 'Continue',
            detail: 'モデル設定に接続情報を入力します。',
          },
          {
            name: 'Aider',
            detail: '起動設定に接続情報を入力します。',
          },
        ],
      },
      {
        id: 'verify',
        title: '確認',
        body: 'エージェントで短いリクエストを送信します。成功した応答が接続を確認します。',
        links: [],
      },
      {
        id: 'troubleshooting',
        title: 'トラブルシューティング',
        body: 'Token、接続アドレス、モデル名が一致しているか確認します。必要に応じて設定を再保存してください。',
        links: [],
      },
    ],
  },
  vi: {
    title: 'Kết nối tác nhân LLMRouter',
    introduction: 'Sử dụng Token để kết nối tác nhân của bạn với LLMRouter.',
    sections: [
      {
        id: 'start',
        title: 'Bắt đầu',
        body: 'Chuẩn bị Token LLMRouter, địa chỉ kết nối và tên mô hình.',
        links: [],
      },
      {
        id: 'get-token',
        title: 'Lấy Token',
        body: 'Đăng nhập để tạo hoặc quản lý Token, rồi giữ nó an toàn.',
        links: [{ href: '/keys', label: 'Quản lý Token' }],
      },
      {
        id: 'connection-address',
        title: 'Địa chỉ kết nối',
        body: 'Sử dụng địa chỉ từ llmrouter.quukk.com trong cài đặt kết nối của tác nhân.',
        links: [],
      },
      {
        id: 'choose-model',
        title: 'Chọn mô hình',
        body: 'Chọn mô hình phù hợp cho tác vụ. Quản lý tuyến trong bảng điều khiển khi cần.',
        links: [
          { href: '/dashboard/models', label: 'Xem mô hình' },
          { href: '/client-model-routes', label: 'Quản lý tuyến' },
        ],
      },
      {
        id: 'configure-agent',
        title: 'Cấu hình tác nhân',
        body: 'Sử dụng tập lệnh cài đặt hoặc nhập Token, địa chỉ kết nối và tên mô hình vào cài đặt tác nhân.',
        links: [{ href: '/install/', label: 'Tập lệnh cài đặt' }],
        agents: [
          {
            name: 'Codex CLI/Desktop',
            detail: 'Làm theo tập lệnh cài đặt để thiết lập kết nối.',
          },
          {
            name: 'Claude Code/Desktop',
            detail:
              'Thiết lập Claude Code bằng tập lệnh cài đặt; Claude Desktop sử dụng quyền truy cập công cụ MCP.',
          },
          {
            name: 'OpenCode',
            detail: 'Nhập thông tin kết nối trong cài đặt nhà cung cấp.',
          },
          {
            name: 'Continue',
            detail: 'Nhập thông tin kết nối trong cài đặt mô hình.',
          },
          {
            name: 'Aider',
            detail: 'Nhập thông tin kết nối trong cấu hình khởi động.',
          },
        ],
      },
      {
        id: 'verify',
        title: 'Xác minh',
        body: 'Gửi một yêu cầu ngắn trong tác nhân. Phản hồi thành công xác nhận kết nối.',
        links: [],
      },
      {
        id: 'troubleshooting',
        title: 'Khắc phục sự cố',
        body: 'Kiểm tra xem Token, địa chỉ kết nối và tên mô hình có khớp không; lưu lại cài đặt nếu cần.',
        links: [],
      },
    ],
  },
  zhTW: {
    title: '連接 LLMRouter Agent',
    introduction: '使用 Token 將常用 Agent 連接到 LLMRouter。',
    sections: [
      {
        id: 'start',
        title: '開始',
        body: '準備一個 LLMRouter Token、連接地址和要使用的模型名稱。',
        links: [],
      },
      {
        id: 'get-token',
        title: '取得 Token',
        body: '登入後建立或管理 Token，並妥善保管它。',
        links: [{ href: '/keys', label: 'Token 管理' }],
      },
      {
        id: 'connection-address',
        title: '連接地址',
        body: '在 Agent 的連接設定中使用 llmrouter.quukk.com 提供的地址。',
        links: [],
      },
      {
        id: 'choose-model',
        title: '選擇模型',
        body: '在可用模型中選擇適合當前任務的模型；如需設定路由，請在控制台管理。',
        links: [
          { href: '/dashboard/models', label: '查看模型' },
          { href: '/client-model-routes', label: '管理路由' },
        ],
      },
      {
        id: 'configure-agent',
        title: '設定 Agent',
        body: '使用安裝指令稿或在 Agent 設定中填入 Token、連接地址和模型名稱。',
        links: [{ href: '/install/', label: '安裝指令稿' }],
        agents: [
          { name: 'Codex CLI/Desktop', detail: '按安裝指令稿完成連接設定。' },
          {
            name: 'Claude Code/Desktop',
            detail:
              'Claude Code 按安裝指令稿設定；Claude Desktop 使用 MCP tool access。',
          },
          { name: 'OpenCode', detail: '在提供方設定中填寫連接資訊。' },
          { name: 'Continue', detail: '在模型設定中填寫連接資訊。' },
          { name: 'Aider', detail: '在啟動設定中填寫連接資訊。' },
        ],
      },
      {
        id: 'verify',
        title: '驗證連接',
        body: '在 Agent 中發起一次簡短請求；成功回傳即表示連接可用。',
        links: [],
      },
      {
        id: 'troubleshooting',
        title: '故障排除',
        body: '檢查 Token、連接地址和模型名稱是否一致；必要時重新儲存設定後再試。',
        links: [],
      },
    ],
  },
}
