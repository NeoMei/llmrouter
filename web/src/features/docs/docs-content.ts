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
export const DOCS_LANGUAGES = ['zh-CN', 'en'] as const

export type DocsLanguage = (typeof DOCS_LANGUAGES)[number]

export type DocsLink = {
  href:
    | '/keys'
    | '/dashboard/models'
    | '/client-model-routes'
    | '/install/'
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
  'zh-CN': {
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
}
